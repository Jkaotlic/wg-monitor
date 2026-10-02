package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

const (
	verifyTempPrefix = ".tmp-wg-monitor-verify."
	// Малый архив в распакованном виде -- десятки МБ. Больше гигабайта --
	// это уже не малый архив, и разворачивать его на карту незачем.
	verifyMaxUnpackedBytes = 1 << 30
)

type backupVerifyOptions struct {
	ConfigPath     string
	PassphraseFile string
	OutDir         string
	LayoutRoot     string

	now    func() time.Time
	stdout io.Writer
}

func runBackupVerifyCommand(args []string) error {
	fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := backupVerifyOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	fs.StringVar(&opts.PassphraseFile, "passphrase-file", "", "path to backup encryption passphrase")
	fs.StringVar(&opts.OutDir, "out-dir", "/var/lib/wg-monitor/backups", "backup output directory")
	fs.StringVar(&opts.LayoutRoot, "layout-root", "", "host root for container-style /data and /secrets paths")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("backup verify: unexpected argument %q", fs.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runBackupVerify(ctx, opts)
}

// verifyCounts -- что сверяется между архивом и живой базой.
type verifyCounts struct {
	Routers   int
	Owners    int
	Operators int
}

// runBackupVerify разворачивает последний малый архив во временный каталог
// и сверяет его с живой системой. Итог пишет в секцию verify файла
// состояния; ошибка -- проверка провалена.
func runBackupVerify(ctx context.Context, opts backupVerifyOptions) error {
	if opts.PassphraseFile == "" {
		return fmt.Errorf("--passphrase-file is required")
	}
	if opts.OutDir == "" {
		return fmt.Errorf("--out-dir is required")
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.stdout == nil {
		opts.stdout = os.Stdout
	}
	cfg, err := loadBackupConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	dbPath := resolveLayoutPath(cfg.DBPath, opts.LayoutRoot)
	var pass string
	archive, counts, verr := func() (string, verifyCounts, error) {
		p, err := readTrimmedFile(opts.PassphraseFile)
		if err != nil {
			return "", verifyCounts{}, fmt.Errorf("парольная фраза бэкапа не прочитана: %w", err)
		}
		if p == "" {
			return "", verifyCounts{}, errors.New("парольная фраза бэкапа пуста")
		}
		pass = p
		return verifyLatestSmall(ctx, opts, cfg, dbPath, []byte(p))
	}()
	if verr != nil && len(pass) >= 6 {
		verr = errors.New(strings.ReplaceAll(verr.Error(), pass, "<redacted>"))
	}
	at := opts.now().UTC().Format(time.RFC3339)
	serr := backup.UpdateStatus(backup.StatusPath(dbPath), func(s *backup.Status) {
		s.Verify = backup.VerifyStatus{LastRunAt: at, OK: verr == nil, Routers: counts.Routers}
		if verr != nil {
			s.Verify.Error = backup.StatusErrorText(verr.Error())
		}
	})
	if serr != nil {
		slog.Error("backup status not written", "err", serr.Error())
		serr = fmt.Errorf("write %s: %w", backup.StatusFileName, serr)
	}
	if verr != nil {
		slog.Error("backup verify failed", "archive", archive, "err", verr.Error())
		return errors.Join(fmt.Errorf("backup verify: %w", verr), serr)
	}
	fmt.Fprintf(opts.stdout, "архив %s восстанавливается: роутеров %d, владельцев %d, операторов %d -- как записано в манифесте при бэкапе\n",
		archive, counts.Routers, counts.Owners, counts.Operators)
	if !includeReviveKey {
		fmt.Fprintln(opts.stdout, reviveKeyNotInBackupNote)
	}
	fmt.Fprintln(opts.stdout, "в малом архиве нет истории событий: после восстановления экран пуст до первого отчёта агентов (до минуты)")
	return serr
}

func verifyLatestSmall(ctx context.Context, opts backupVerifyOptions, cfg *backend.Config, dbPath string, pass []byte) (archive string, counts verifyCounts, err error) {
	archive, err = latestArchive(opts.OutDir, backup.KindSmall)
	if err != nil {
		return "", counts, err
	}
	// Временный каталог -- в --out-dir: тот же том и не /tmp в памяти.
	tmpDir, err := os.MkdirTemp(opts.OutDir, verifyTempPrefix)
	if err != nil {
		return archive, counts, fmt.Errorf("не удалось создать временный каталог: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	members, err := extractEncryptedArchive(ctx, filepath.Join(opts.OutDir, archive), pass, tmpDir, verifyMaxUnpackedBytes)
	if err != nil {
		return archive, counts, fmt.Errorf("архив не разворачивается: %w", err)
	}
	for _, required := range []string{archiveStateDB, archiveBackendYAML, archiveManifest} {
		if !slices.Contains(members, required) {
			return archive, counts, fmt.Errorf("в архиве нет файла %s", required)
		}
	}

	restored, err := openSQLiteForRead(ctx, filepath.Join(tmpDir, archiveStateDB))
	if err != nil {
		return archive, counts, fmt.Errorf("база из архива не открывается: %w", err)
	}
	defer restored.Close()
	var check string
	if err := restored.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil {
		return archive, counts, fmt.Errorf("база из архива не проверяется: %w", err)
	}
	if check != "ok" {
		return archive, counts, fmt.Errorf("база из архива повреждена: %s", check)
	}
	counts, err = readVerifyCounts(ctx, restored)
	if err != nil {
		return archive, counts, fmt.Errorf("база из архива не читается: %w", err)
	}
	// Сверка со счётчиками из манифеста, записанными в момент бэкапа, а не с
	// живой базой: за неделю между бэкапом и проверкой роутеры добавляются.
	want, err := readManifestCounts(filepath.Join(tmpDir, archiveManifest))
	if err != nil {
		return archive, counts, err
	}
	for _, c := range []struct {
		what       string
		got, wants int
	}{
		{"роутеров", counts.Routers, want.Routers},
		{"владельцев", counts.Owners, want.Owners},
		{"операторов", counts.Operators, want.Operators},
	} {
		if c.got != c.wants {
			return archive, counts, fmt.Errorf("в архиве %s %d, в манифесте архива %d", c.what, c.got, c.wants)
		}
	}

	// Хранилища: всё, что лежит в живой системе, обязано быть в архиве и
	// разбираться как JSON.
	for _, st := range cfg.StoreFiles() {
		inArchive := slices.Contains(members, st.Name)
		if info, err := os.Stat(resolveLayoutPath(st.Path, opts.LayoutRoot)); err == nil && info.Mode().IsRegular() && !inArchive {
			return archive, counts, fmt.Errorf("в архиве нет хранилища %s", st.Name)
		}
		if !inArchive {
			continue
		}
		body, err := os.ReadFile(filepath.Join(tmpDir, st.Name)) // #nosec G304 -- имя из постоянного списка хранилищ
		if err != nil {
			return archive, counts, fmt.Errorf("хранилище %s из архива не читается: %w", st.Name, err)
		}
		if !json.Valid(body) {
			return archive, counts, fmt.Errorf("хранилище %s из архива -- не JSON", st.Name)
		}
	}

	// Ключ оживления в архиве по правилу не лежит (includeReviveKey), и
	// проверять нечего. Если оператор включит его в архив -- проверяется.
	if includeReviveKey {
		if err := verifyReviveKey(ctx, restored, tmpDir, members, resolveLayoutPath(cfg.Revive.KeyFile, opts.LayoutRoot)); err != nil {
			return archive, counts, err
		}
	}
	return archive, counts, nil
}

// readManifestCounts достаёт из манифеста счётчики роутеров, владельцев и
// операторов, записанные при сборке архива.
func readManifestCounts(path string) (verifyCounts, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- файл во временном каталоге проверки
	if err != nil {
		return verifyCounts{}, fmt.Errorf("манифест архива не читается: %w", err)
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[k] = v
		}
	}
	var c verifyCounts
	for _, f := range []struct {
		key string
		dst *int
	}{{"routers", &c.Routers}, {"owners", &c.Owners}, {"operators", &c.Operators}} {
		n, err := strconv.Atoi(kv[f.key])
		if err != nil || n < 0 {
			return verifyCounts{}, errors.New("в манифесте архива нет счётчиков (" + f.key + ") -- архив собран без них, сверить нечем")
		}
		*f.dst = n
	}
	return c, nil
}

// verifyReviveKey: ключ оживления из архива читается и расшифровывает хотя
// бы один сохранённый пароль роутера, если такие в базе архива есть.
func verifyReviveKey(ctx context.Context, restored *sql.DB, tmpDir string, members []string, liveKeyPath string) error {
	inArchive := slices.Contains(members, archiveReviveKey)
	if info, err := os.Stat(liveKeyPath); liveKeyPath != "" && err == nil && info.Mode().IsRegular() && !inArchive {
		return errors.New("в архиве нет ключа оживления revive.key")
	}
	var hasTable int
	if err := restored.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'router_credentials'`).Scan(&hasTable); err != nil {
		return fmt.Errorf("база из архива не читается: %w", err)
	}
	type credential struct {
		routerID          int64
		nonce, ciphertext []byte
	}
	var creds []credential
	if hasTable > 0 {
		rows, err := restored.QueryContext(ctx, `SELECT user_id, nonce, ciphertext FROM router_credentials`)
		if err != nil {
			return fmt.Errorf("сохранённые пароли роутеров из архива не читаются: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c credential
			if err := rows.Scan(&c.routerID, &c.nonce, &c.ciphertext); err != nil {
				return fmt.Errorf("сохранённые пароли роутеров из архива не читаются: %w", err)
			}
			creds = append(creds, c)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("сохранённые пароли роутеров из архива не читаются: %w", err)
		}
	}
	if !inArchive {
		if len(creds) > 0 {
			return fmt.Errorf("в архиве %d сохранённых паролей роутеров, но нет ключа revive.key -- их не расшифровать", len(creds))
		}
		return nil
	}
	key, err := revive.LoadKey(filepath.Join(tmpDir, archiveReviveKey))
	if err != nil {
		return fmt.Errorf("ключ оживления из архива негоден: %w", err)
	}
	defer clear(key)
	if len(creds) == 0 {
		return nil
	}
	box, err := revive.NewBox(key)
	if err != nil {
		return fmt.Errorf("ключ оживления из архива негоден: %w", err)
	}
	for _, c := range creds {
		if _, err := box.Open(c.routerID, c.nonce, c.ciphertext); err == nil {
			return nil
		}
	}
	return fmt.Errorf("ключ оживления из архива не расшифровывает ни один из %d сохранённых паролей роутеров", len(creds))
}

func openSQLiteForRead(ctx context.Context, path string) (*sql.DB, error) {
	if info, err := os.Stat(path); err != nil {
		return nil, err
	} else if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// readVerifyCounts: роутеры -- строки users; владельцы -- разные
// telegram_user_id у роутеров; операторы -- строки router_operators.
func readVerifyCounts(ctx context.Context, db *sql.DB) (verifyCounts, error) {
	var c verifyCounts
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&c.Routers); err != nil {
		return c, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(DISTINCT telegram_user_id) FROM users WHERE telegram_user_id IS NOT NULL AND telegram_user_id != 0`).Scan(&c.Owners); err != nil {
		return c, err
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM router_operators`).Scan(&c.Operators); err != nil {
		return c, err
	}
	return c, nil
}

// latestArchive -- имя самого свежего архива вида kind в каталоге dir.
// Время берётся из имени; архивы «из будущего» тоже годятся.
func latestArchive(dir string, kind backup.Kind) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("каталог архивов не читается: %w", err)
	}
	var (
		best   string
		bestAt time.Time
	)
	for _, entry := range entries {
		k, at, ok := backup.ParseArchiveName(entry.Name())
		if !ok || k != kind || !entry.Type().IsRegular() {
			continue
		}
		if best == "" || at.After(bestAt) {
			best, bestAt = entry.Name(), at
		}
	}
	if best == "" {
		return "", errors.New("малого архива ещё нет -- проверять нечего")
	}
	return best, nil
}

// runBackupExtractCommand -- `backup extract`: расшифровать архив (малый
// или полный, v1 или v2) в каталог. Нужен для восстановления руками там,
// где мастер не работает (докер-раскладка на Pi).
func runBackupExtractCommand(args []string) error {
	fs := flag.NewFlagSet("backup extract", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var archive, passFile, to string
	fs.StringVar(&archive, "archive", "", "encrypted backup archive (.tgz.enc)")
	fs.StringVar(&passFile, "passphrase-file", "", "path to backup encryption passphrase")
	fs.StringVar(&to, "to", "", "directory to extract into (must be empty or absent)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if archive == "" || passFile == "" || to == "" {
		return errors.New("usage: backup extract --archive <file.tgz.enc> --passphrase-file <file> --to <dir>")
	}
	pass, err := readTrimmedFile(passFile)
	if err != nil {
		return fmt.Errorf("read passphrase: %w", err)
	}
	if pass == "" {
		return errors.New("backup passphrase is empty")
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return err
	}
	if entries, err := os.ReadDir(to); err != nil {
		return err
	} else if len(entries) > 0 {
		return fmt.Errorf("%s is not empty", to)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	names, err := extractEncryptedArchive(ctx, archive, []byte(pass), to, 0)
	if err != nil {
		// Половина архива хуже, чем ничего: по ней могут начать восстанавливать.
		for _, name := range names {
			_ = os.Remove(filepath.Join(to, name))
		}
		return fmt.Errorf("backup extract: %w", err)
	}
	for _, name := range names {
		fmt.Println(filepath.Join(to, name))
	}
	return nil
}
