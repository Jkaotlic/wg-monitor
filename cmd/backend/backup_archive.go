package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
	"github.com/Jkaotlic/wg-monitor/internal/backup/smalldb"
)

// Имена файлов внутри архива. Под ними же их ищет восстановление
// (cmd/deploy/restore_backup.go) и проверка (`backup verify`).
const (
	archiveStateDB        = "state.db"
	archiveBackendYAML    = "backend.yaml"
	archiveBotToken       = "bot-token.txt"       // #nosec G101 -- имя файла в архиве, не секрет
	archiveWizardToken    = "wizard-token.txt"    // #nosec G101 -- имя файла в архиве, не секрет
	archiveDashboardToken = "dashboard-token.txt" // #nosec G101 -- имя файла в архиве, не секрет
	archiveAgentsCSV      = "agents.csv"
	archiveManifest       = "manifest.txt"
	archiveOperatorVault  = "operator-secrets.tgz.enc"
	archiveReviveKey      = "revive.key"
)

// includeReviveKey -- единственный выключатель: едет ли ключ оживления агента
// (revive.key) в архивы. По правилу нет: ключ живёт отдельно от базы, бэкап с
// базой и конфигом не должен нести его ни отдельным файлом, ни внутри другого,
// иначе утёкший бэкап расшифровывал бы сохранённые пароли роутеров. Цена:
// после восстановления эти пароли вводятся заново. Если оператор решит иначе,
// достаточно true здесь (и вернуть чтение ключа в cmd/deploy/restore_backup.go,
// убранное тем же коммитом).
var includeReviveKey = false

// archiveMember -- файл, который едет в архив под именем Name.
type archiveMember struct {
	Name string
	Path string
}

// stageMembers готовит состав архива вида kind. В tmpDir попадает только
// то, что приходится создавать: копия базы, agents.csv и манифест. Файлы с
// секретами читаются с их мест прямо в поток архива и на диск в открытом
// виде второй раз не ложатся.
func (e *backupEnv) stageMembers(ctx context.Context, kind backup.Kind, tmpDir string) ([]archiveMember, error) {
	opts, cfg := e.opts, e.cfg
	dbCopy := filepath.Join(tmpDir, archiveStateDB)
	// Счётчики манифеста: малая база считает их в ИСТОЧНИКЕ, в той же
	// транзакции, что и перенос; полная -- из снимка VACUUM INTO, который
	// SQLite сам делает одним срезом. Считать собранную нами копию нельзя:
	// сверка копии с её же числами ничего не доказывала бы.
	counts := verifyCounts{Routers: -1, Owners: -1, Operators: -1}
	switch kind {
	case backup.KindSmall:
		c, err := smalldb.BuildCounted(ctx, e.dbPath, dbCopy)
		if err != nil {
			return nil, fmt.Errorf("small database: %w", err)
		}
		if c.Known {
			counts = verifyCounts{Routers: c.Routers, Owners: c.Owners, Operators: c.Operators}
		}
	default:
		if err := vacuumSQLite(ctx, e.dbPath, dbCopy); err != nil {
			return nil, err
		}
		if c, err := countArchivedDB(ctx, dbCopy); err == nil {
			counts = c
		}
	}
	if counts.Routers < 0 {
		slog.Warn("backup manifest counts not written", "kind", string(kind))
	}
	members := []archiveMember{
		{archiveStateDB, dbCopy},
		{archiveBackendYAML, opts.ConfigPath},
	}
	// Токен бота обязателен, прочее -- если настроено и лежит на месте.
	botTokenPath := resolveLayoutPath(cfg.Telegram.BotTokenFile, opts.LayoutRoot)
	if _, err := os.Stat(botTokenPath); err != nil {
		return nil, fmt.Errorf("bot token file: %w", err)
	}
	members = append(members, archiveMember{archiveBotToken, botTokenPath})
	optional := func(name, path string) (bool, error) {
		if strings.TrimSpace(path) == "" {
			return false, nil
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("stat %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return false, nil
		}
		members = append(members, archiveMember{name, path})
		return true, nil
	}
	if wizardTokenPath := resolveLayoutPath(cfg.Wizard.TokenFile, opts.LayoutRoot); wizardTokenPath != "" {
		// Заданный в конфиге мастер-токен обязан существовать -- как раньше.
		if _, err := os.Stat(wizardTokenPath); err != nil {
			return nil, fmt.Errorf("wizard token file: %w", err)
		}
		members = append(members, archiveMember{archiveWizardToken, wizardTokenPath})
	}
	if _, err := optional(archiveDashboardToken, resolveLayoutPath(cfg.Dashboard.TokenFile, opts.LayoutRoot)); err != nil {
		return nil, err
	}
	if _, err := optional(archiveOperatorVault, opts.OperatorVault); err != nil {
		return nil, err
	}
	// JSON-хранилища вне базы: панели, свои серверы, ключи кабинетов, коды
	// HideMy. Файла нет -- не ошибка: хранилище ещё не заводили.
	var stores []string
	for _, st := range cfg.StoreFiles() {
		ok, err := optional(st.Name, resolveLayoutPath(st.Path, opts.LayoutRoot))
		if err != nil {
			return nil, err
		}
		if ok {
			stores = append(stores, st.Name)
		}
	}
	// Ключ оживления по правилу в архив не кладётся (см. includeReviveKey).
	hasKey := false
	if includeReviveKey {
		var err error
		hasKey, err = optional(archiveReviveKey, resolveLayoutPath(cfg.Revive.KeyFile, opts.LayoutRoot))
		if err != nil {
			return nil, err
		}
	}
	agents := filepath.Join(tmpDir, archiveAgentsCSV)
	if err := writeAgentsCSV(dbCopy, agents); err != nil {
		return nil, err
	}
	manifest := filepath.Join(tmpDir, archiveManifest)
	if err := writeManifest(manifest, manifestInfo{
		Kind:       kind,
		Stamp:      e.now.Format("20060102T150405Z"),
		DBPath:     cfg.DBPath,
		ConfigPath: opts.ConfigPath,
		Stores:     stores,
		ReviveKey:  hasKey,
		Counts:     counts,
	}); err != nil {
		return nil, err
	}
	return append(members, archiveMember{archiveAgentsCSV, agents}, archiveMember{archiveManifest, manifest}), nil
}

// vacuumSQLite снимает согласованную копию всей базы в файл dst.
func vacuumSQLite(ctx context.Context, src, dst string) error {
	if info, err := os.Stat(src); err != nil {
		return fmt.Errorf("source database: %w", err)
	} else if !info.Mode().IsRegular() {
		return errors.New("source database is not a regular file")
	}
	db, err := sql.Open("sqlite", src)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("busy_timeout: %w", err)
	}
	escaped := strings.ReplaceAll(dst, "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil { // #nosec G202 -- путь временного файла, кавычки экранированы
		return fmt.Errorf("vacuum sqlite: %w", err)
	}
	return nil
}

type manifestInfo struct {
	Kind       backup.Kind
	Stamp      string
	DBPath     string
	ConfigPath string
	Stores     []string
	ReviveKey  bool
	Counts     verifyCounts
}

// countArchivedDB считает роутеры, владельцев и операторов в базе, которая
// едет в архив (копия сделана одной транзакцией -- это срез на момент бэкапа).
func countArchivedDB(ctx context.Context, dbCopy string) (verifyCounts, error) {
	d, err := openSQLiteForRead(ctx, dbCopy)
	if err != nil {
		return verifyCounts{}, err
	}
	defer d.Close()
	return readVerifyCounts(ctx, d)
}

// Тексты про ключ оживления: в манифесте и в выводе проверки.
const reviveKeyNotInBackupNote = "ключа оживления в бэкапе нет: после восстановления сохранённые пароли роутеров вводятся заново"

// writeManifest пишет паспорт архива. В нём только имена и пути: содержимое
// хранилищ и ключа -- секреты.
func writeManifest(path string, m manifestInfo) error {
	reviveKey, reviveNote, skipped := "no", reviveKeyNotInBackupNote, ""
	if m.ReviveKey {
		reviveKey, reviveNote = "yes", ""
	}
	if m.Kind == backup.KindSmall {
		skipped = strings.Join(smalldb.SkipTables, ",")
	}
	var b strings.Builder
	for _, kv := range [][2]string{
		{"name", "wg-monitor-" + string(m.Kind) + "-backup"},
		{"kind", string(m.Kind)},
		{"created_utc", m.Stamp},
		{"backend_version", Version},
		{"db_path", m.DBPath},
		{"config_path", m.ConfigPath},
		{"host", hostname()},
		{"format", "encrypted-" + string(m.Kind) + "-v2"},
		{"stores", strings.Join(m.Stores, ",")},
		{"revive_key", reviveKey},
		{"revive_key_note", reviveNote},
		{"routers", countText(m.Counts.Routers)},
		{"owners", countText(m.Counts.Owners)},
		{"operators", countText(m.Counts.Operators)},
		{"skipped_tables", skipped},
	} {
		b.WriteString(kv[0] + "=" + kv[1] + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// countText: отрицательное значение -- «не посчиталось».
func countText(n int) string {
	if n < 0 {
		return "unknown"
	}
	return strconv.Itoa(n)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// ctxReader прерывает долгую запись архива по отмене контекста (SIGTERM).
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// writeEncryptedArchive пишет архив потоком: файл -> tar -> gzip -> шифр v2
// -> диск. В памяти одновременно живут только буферы потока (единицы МБ),
// сколько бы ни весила база. dst не должен существовать; при ошибке
// недописанный файл удаляет вызывающий.
func writeEncryptedArchive(ctx context.Context, dst string, members []archiveMember, pass []byte, params backup.Params) (err error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- путь в --out-dir
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	bw := bufio.NewWriterSize(out, 256<<10)
	enc, err := backup.NewEncryptWriter(bw, pass, params)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)
	buf := make([]byte, 256<<10)
	for _, m := range members {
		if err := addArchiveMember(ctx, tw, m, buf); err != nil {
			return fmt.Errorf("%s: %w", m.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return out.Sync()
}

func addArchiveMember(ctx context.Context, tw *tar.Writer, m archiveMember, buf []byte) error {
	f, err := os.Open(m.Path) // #nosec G304 -- пути из конфига бэкенда и временного каталога прогона
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if err := tw.WriteHeader(&tar.Header{Name: m.Name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		return err
	}
	// Ровно info.Size() байт: файл, выросший за время копирования, не
	// ломает архив, а укоротившийся -- честная ошибка.
	n, err := io.CopyBuffer(tw, io.LimitReader(ctxReader{ctx, f}, info.Size()), buf)
	if err != nil {
		return err
	}
	if n != info.Size() {
		return fmt.Errorf("file shrank while being archived: %d of %d bytes", n, info.Size())
	}
	return nil
}

// extractEncryptedArchive расшифровывает архив (v1 или v2) и раскладывает
// его файлы в dstDir с правами 0600. Принимает только обычные файлы с
// простыми именами; повтор имени -- ошибка. maxBytes > 0 ограничивает
// суммарный размер распакованного. Возвращает имена созданных файлов --
// и при ошибке тоже, чтобы вызывающий мог убрать недоразвёрнутое.
func extractEncryptedArchive(ctx context.Context, archivePath string, pass []byte, dstDir string, maxBytes int64) (names []string, err error) {
	in, err := os.Open(archivePath) // #nosec G304 -- архив из --out-dir или указан оператором
	if err != nil {
		return nil, err
	}
	defer in.Close()
	dec, err := backup.NewDecryptReader(ctxReader{ctx, in}, pass)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var total int64
	buf := make([]byte, 256<<10)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return names, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := hdr.Name
		if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\`) {
			return names, fmt.Errorf("archive contains suspect path %q", hdr.Name)
		}
		if seen[name] {
			return names, fmt.Errorf("archive contains duplicate member %q", name)
		}
		seen[name] = true
		total += hdr.Size
		if maxBytes > 0 && total > maxBytes {
			return names, fmt.Errorf("archive is larger than %d bytes unpacked", maxBytes)
		}
		f, err := os.OpenFile(filepath.Join(dstDir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- имя проверено выше
		if err != nil {
			return names, err
		}
		names = append(names, name)
		_, err = io.CopyBuffer(f, io.LimitReader(tr, hdr.Size), buf)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return names, fmt.Errorf("extract %s: %w", name, err)
		}
	}
	// tar останавливается на своём конце, не дочитывая поток. Дочитываем:
	// только так проверяются последний блок шифра и отсутствие хвоста.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return names, fmt.Errorf("gzip: %w", err)
	}
	if _, err := io.Copy(io.Discard, dec); err != nil {
		return names, err
	}
	return names, nil
}
