package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

// backupCommandOptions -- флаги `wg-monitor-backend backup`.
type backupCommandOptions struct {
	ConfigPath     string
	PassphraseFile string
	OperatorVault  string
	OutDir         string
	LayoutRoot     string
	SendTelegram   bool
	LimitBytes     int64
	TestKDF        bool

	// Kind -- какой архив делать: small, full или both.
	Kind string
	// OffsiteSCP -- куда копировать полный архив (user@host:path); пусто --
	// внешняя цель не настроена. OffsiteKey -- файл ключа SSH для неё.
	OffsiteSCP string
	OffsiteKey string
	// Хранение. -1 -- значение не задано: берётся более общее, затем
	// умолчание вида (малый 7/4, полный 3/0).
	KeepDaily       int
	KeepWeekly      int
	SmallKeepDaily  int
	SmallKeepWeekly int
	FullKeepDaily   int
	FullKeepWeekly  int

	// Швы для тестов; nil -- настоящие часы, Telegram и запуск команд.
	now          func() time.Time
	sendDocument func(ctx context.Context, token string, chatID int64, path, caption string) error
	runCommand   backupExecFunc
	stdout       io.Writer
}

// backupExecFunc запускает внешнюю команду и возвращает её общий вывод.
type backupExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

const (
	backupKindSmall = "small"
	backupKindFull  = "full"
	backupKindBoth  = "both"

	// Временные каталоги и недописанные архивы живут в --out-dir: тот же
	// том, что и готовые архивы, и никогда не /tmp (он бывает в памяти).
	backupTempPrefix    = ".tmp-wg-monitor-backup."
	backupPartialSuffix = ".partial"
	// Старше этого возраста временные файлы считаются брошенными убитым
	// прогоном и убираются на старте следующего.
	backupStaleAfter = 6 * time.Hour
	// Сколько ждём scp: 2 ГБ по медленному каналу -- это десятки минут.
	backupOffsiteTimeout = time.Hour
)

func runBackupCommand(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "verify":
			return runBackupVerifyCommand(args[1:])
		case "extract":
			return runBackupExtractCommand(args[1:])
		}
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := backupCommandOptions{}
	fs.StringVar(&opts.ConfigPath, "config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	fs.StringVar(&opts.PassphraseFile, "passphrase-file", "", "path to backup encryption passphrase")
	fs.StringVar(&opts.OperatorVault, "operator-vault", "", "optional encrypted operator secrets vault")
	fs.StringVar(&opts.OutDir, "out-dir", "/var/lib/wg-monitor/backups", "backup output directory")
	fs.StringVar(&opts.LayoutRoot, "layout-root", "", "host root for container-style /data and /secrets paths")
	fs.StringVar(&opts.Kind, "kind", backupKindBoth, "which archive to make: small, full or both")
	fs.BoolVar(&opts.SendTelegram, "send-telegram", false, "send the small encrypted backup to Telegram")
	fs.Int64Var(&opts.LimitBytes, "limit-bytes", 47185920, "Telegram document size limit (small archive)")
	fs.StringVar(&opts.OffsiteSCP, "offsite-scp", "", "copy the full archive to user@host:path with scp")
	fs.StringVar(&opts.OffsiteKey, "offsite-key", "", "SSH private key file for --offsite-scp")
	fs.IntVar(&opts.KeepDaily, "keep-daily", -1, "daily archives to keep, for every kind in this run")
	fs.IntVar(&opts.KeepWeekly, "keep-weekly", -1, "weekly archives to keep, for every kind in this run")
	fs.IntVar(&opts.SmallKeepDaily, "small-keep-daily", -1, "daily small archives to keep (default 7)")
	fs.IntVar(&opts.SmallKeepWeekly, "small-keep-weekly", -1, "weekly small archives to keep (default 4)")
	fs.IntVar(&opts.FullKeepDaily, "full-keep-daily", -1, "daily full archives to keep (default 3)")
	fs.IntVar(&opts.FullKeepWeekly, "full-keep-weekly", -1, "weekly full archives to keep (default 0)")
	fs.BoolVar(&opts.TestKDF, "test-kdf", false, "use lightweight KDF params for tests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("backup: unexpected argument %q", fs.Arg(0))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runBackup(ctx, opts)
}

// retentionFor -- правило хранения вида: свой флаг, затем общий, затем умолчание.
func (o backupCommandOptions) retentionFor(kind backup.Kind) backup.Retention {
	r := backup.DefaultRetention(kind)
	daily, weekly := o.SmallKeepDaily, o.SmallKeepWeekly
	if kind == backup.KindFull {
		daily, weekly = o.FullKeepDaily, o.FullKeepWeekly
	}
	for _, v := range []int{o.KeepDaily, daily} {
		if v >= 0 {
			r.KeepDaily = v
		}
	}
	for _, v := range []int{o.KeepWeekly, weekly} {
		if v >= 0 {
			r.KeepWeekly = v
		}
	}
	return r
}

// backupEnv -- всё, что нужно прогону одного вида.
type backupEnv struct {
	opts       backupCommandOptions
	cfg        *backend.Config
	pass       []byte
	params     backup.Params
	dbPath     string
	botToken   string // для вычистки из текстов ошибок; читается лениво
	statusPath string
	now        time.Time
}

// kindResult -- итог прогона одного вида; из него пишется секция состояния.
type kindResult struct {
	file     string
	size     int64
	telegram string
	offsite  string
	err      error
}

// runBackup делает архивы запрошенных видов. Провал одного вида не отменяет
// другой; ошибка возвращается, если не удался хотя бы один. После каждого
// вида его итог сразу уходит в backup-status.json: если следующий вид
// убьют по памяти, про первый уже записано.
func runBackup(ctx context.Context, opts backupCommandOptions) error {
	if opts.PassphraseFile == "" {
		return fmt.Errorf("--passphrase-file is required")
	}
	if opts.OutDir == "" {
		return fmt.Errorf("--out-dir is required")
	}
	if opts.Kind == "" {
		opts.Kind = backupKindBoth
	}
	var kinds []backup.Kind
	switch opts.Kind {
	case backupKindSmall:
		kinds = []backup.Kind{backup.KindSmall}
	case backupKindFull:
		kinds = []backup.Kind{backup.KindFull}
	case backupKindBoth:
		kinds = []backup.Kind{backup.KindSmall, backup.KindFull}
	default:
		return fmt.Errorf("--kind must be small, full or both, got %q", opts.Kind)
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.sendDocument == nil {
		opts.sendDocument = sendTelegramDocument
	}
	if opts.runCommand == nil {
		opts.runCommand = runExternalCommand
	}
	if opts.stdout == nil {
		opts.stdout = os.Stdout
	}
	cfg, err := loadBackupConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	env := &backupEnv{
		opts:   opts,
		cfg:    cfg,
		params: backup.DefaultParams(),
		dbPath: resolveLayoutPath(cfg.DBPath, opts.LayoutRoot),
		now:    opts.now().UTC(),
	}
	if opts.TestKDF {
		env.params = backup.TestParams()
	}
	env.statusPath = backup.StatusPath(env.dbPath)
	if token, err := readTrimmedFile(resolveLayoutPath(cfg.Telegram.BotTokenFile, opts.LayoutRoot)); err == nil {
		env.botToken = token
	}

	// Общая подготовка. Её провал -- провал каждого запрошенного вида.
	prepErr := func() error {
		pass, err := readTrimmedFile(opts.PassphraseFile)
		if err != nil {
			return fmt.Errorf("read passphrase: %w", err)
		}
		if pass == "" {
			return fmt.Errorf("backup passphrase is empty")
		}
		env.pass = []byte(pass)
		if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
			return err
		}
		removeStaleBackupTemps(opts.OutDir, env.now)
		return nil
	}()

	var errs []error
	for _, kind := range kinds {
		var res kindResult
		if prepErr != nil {
			res = kindResult{telegram: backup.DeliveryOff, err: prepErr}
		} else {
			res = env.runKind(ctx, kind)
		}
		if res.err != nil {
			res.err = errors.New(env.redact(res.err.Error()))
			errs = append(errs, fmt.Errorf("%s backup: %w", kind, res.err))
			slog.Error("backup failed", "kind", string(kind), "err", res.err.Error())
		} else {
			slog.Info("backup done", "kind", string(kind), "file", res.file, "size_bytes", res.size,
				"telegram", res.telegram, "offsite", res.offsite)
		}
		if err := env.writeKindStatus(kind, res); err != nil {
			// Архив важнее файла состояния, но молчать нельзя: без него
			// бэкенд покажет вчерашний день.
			slog.Error("backup status not written", "err", err.Error())
			errs = append(errs, fmt.Errorf("write %s: %w", backup.StatusFileName, err))
		}
	}
	return errors.Join(errs...)
}

func (e *backupEnv) writeKindStatus(kind backup.Kind, res kindResult) error {
	at := e.opts.now().UTC().Format(time.RFC3339)
	return backup.UpdateStatus(e.statusPath, func(s *backup.Status) {
		section := &s.Small
		if kind == backup.KindFull {
			section = &s.Full
		}
		lastOK := section.LastOKAt
		*section = backup.KindStatus{
			LastOKAt:  lastOK,
			LastRunAt: at,
			OK:        res.err == nil,
			SizeBytes: res.size,
			File:      res.file,
			Telegram:  res.telegram,
		}
		if kind == backup.KindFull {
			section.Offsite = res.offsite
		}
		if res.err == nil {
			section.LastOKAt = at
		} else {
			section.Error = backup.StatusErrorText(res.err.Error())
		}
	})
}

// redact вычищает из текста секреты, которые могли попасть в него из чужих
// ошибок: токен бота (он часть адреса Telegram API) и парольную фразу.
func (e *backupEnv) redact(msg string) string {
	msg = redactTelegramBotToken(msg, e.botToken)
	for _, secret := range []string{e.botToken, string(e.pass)} {
		if len(secret) >= 6 {
			msg = strings.ReplaceAll(msg, secret, "<redacted>")
		}
	}
	return msg
}

// runKind делает один архив: база и побочные файлы -> поток tar+gzip+шифр
// прямо в файл -> доставка -> чистка старых.
func (e *backupEnv) runKind(ctx context.Context, kind backup.Kind) (res kindResult) {
	opts := e.opts
	res.telegram = backup.DeliveryOff
	if kind == backup.KindFull {
		res.offsite = backup.DeliveryOff
	}

	tmpDir, err := os.MkdirTemp(opts.OutDir, backupTempPrefix)
	if err != nil {
		res.err = fmt.Errorf("не удалось создать временный каталог: %w", err)
		return res
	}
	defer os.RemoveAll(tmpDir)

	members, err := e.stageMembers(ctx, kind, tmpDir)
	if err != nil {
		res.err = fmt.Errorf("архив не собран: %w", err)
		return res
	}
	name := backup.ArchiveName(kind, e.now)
	outPath := filepath.Join(opts.OutDir, name)
	partial := outPath + backupPartialSuffix
	if err := writeEncryptedArchive(ctx, partial, members, e.pass, e.params); err != nil {
		_ = os.Remove(partial)
		res.err = fmt.Errorf("архив не записан: %w", err)
		return res
	}
	if err := os.Rename(partial, outPath); err != nil {
		_ = os.Remove(partial)
		res.err = fmt.Errorf("архив не записан: %w", err)
		return res
	}
	info, err := os.Stat(outPath)
	if err != nil {
		res.err = fmt.Errorf("архив не записан: %w", err)
		return res
	}
	res.file, res.size = name, info.Size()
	fmt.Fprintln(opts.stdout, outPath)

	// Старые архивы убираем только теперь, когда новый уже лежит на диске.
	e.applyRetention(kind)

	switch kind {
	case backup.KindSmall:
		if opts.SendTelegram {
			if err := e.sendSmall(ctx, outPath, res.size); err != nil {
				res.telegram = backup.DeliveryError
				res.err = err
				return res
			}
			res.telegram = backup.DeliveryOK
		}
	case backup.KindFull:
		if strings.TrimSpace(opts.OffsiteSCP) != "" {
			if err := e.copyOffsite(ctx, outPath); err != nil {
				res.offsite = backup.DeliveryError
				res.err = err
				return res
			}
			res.offsite = backup.DeliveryOK
		}
	}
	return res
}

// sendSmall отправляет малый архив админу. Превышение лимита -- ошибка
// прогона, а не тихий пропуск: иначе копий вне сервера не будет месяцами.
func (e *backupEnv) sendSmall(ctx context.Context, path string, size int64) error {
	opts := e.opts
	chatID := e.cfg.Telegram.AdminUserID
	if chatID == 0 {
		chatID = e.cfg.Telegram.ChatID
	}
	if chatID == 0 {
		return fmt.Errorf("архив не ушёл в Telegram: в конфиге нет admin_user_id")
	}
	if opts.LimitBytes > 0 && size > opts.LimitBytes {
		return fmt.Errorf("архив не ушёл в Telegram: он %s, а Telegram принимает до %s", humanBytes(size), humanBytes(opts.LimitBytes))
	}
	token, err := readTrimmedFile(resolveLayoutPath(e.cfg.Telegram.BotTokenFile, opts.LayoutRoot))
	if err != nil {
		return fmt.Errorf("архив не ушёл в Telegram: токен бота не прочитан: %w", err)
	}
	e.botToken = token
	caption := fmt.Sprintf("Малый бэкап wg-monitor %s · %s. Зашифрован паролем восстановления.",
		e.now.Format("02.01.2006 15:04 UTC"), humanBytes(size))
	if err := opts.sendDocument(ctx, token, chatID, path, caption); err != nil {
		return fmt.Errorf("архив не ушёл в Telegram: %w", err)
	}
	return nil
}

// copyOffsite копирует полный архив на внешний сервер через scp. Ключ
// передаётся путём к файлу; его содержимое процесс не читает и не печатает.
func (e *backupEnv) copyOffsite(ctx context.Context, path string) error {
	target := strings.TrimSpace(e.opts.OffsiteSCP)
	key := strings.TrimSpace(e.opts.OffsiteKey)
	if err := validateOffsiteTarget(target); err != nil {
		return fmt.Errorf("архив не скопирован на внешний сервер: %w", err)
	}
	if key == "" || strings.HasPrefix(key, "-") {
		return fmt.Errorf("архив не скопирован на внешний сервер: нужен --offsite-key с файлом ключа SSH")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("архив не скопирован на внешний сервер: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, backupOffsiteTimeout)
	defer cancel()
	out, err := e.opts.runCommand(ctx, "scp",
		"-B",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=20",
		"-i", key,
		abs, target)
	if err != nil {
		detail := lastLine(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("архив не скопирован на внешний сервер: %s", detail)
	}
	return nil
}

// validateOffsiteTarget принимает только user@host:path. Строка уходит
// аргументом в scp: начинающаяся с дефиса стала бы его ключом.
func validateOffsiteTarget(target string) error {
	host, path, ok := strings.Cut(target, ":")
	user, hostname, hasUser := strings.Cut(host, "@")
	if !ok || !hasUser || user == "" || hostname == "" || path == "" ||
		strings.HasPrefix(target, "-") || strings.ContainsAny(target, " \t\r\n") {
		return fmt.Errorf("--offsite-scp должен быть вида user@host:path")
	}
	return nil
}

func runExternalCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- имя команды постоянное (scp), аргументы собраны кодом и проверены.
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// humanBytes -- размер словами для человека: «4,1 МБ».
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f МБ", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	default:
		return fmt.Sprintf("%d Б", n)
	}
}

// applyRetention удаляет лишние архивы вида kind из --out-dir. Сбой удаления
// прогон не валит: новый архив уже сделан.
func (e *backupEnv) applyRetention(kind backup.Kind) {
	entries, err := os.ReadDir(e.opts.OutDir)
	if err != nil {
		slog.Warn("backup retention: list failed", "err", err.Error())
		return
	}
	var names []string
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			names = append(names, entry.Name())
		}
	}
	removed := 0
	for _, name := range backup.SelectForDeletion(kind, names, e.now, e.opts.retentionFor(kind)) {
		if err := os.Remove(filepath.Join(e.opts.OutDir, name)); err != nil {
			slog.Warn("backup retention: remove failed", "file", name, "err", err.Error())
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.Info("backup retention: old archives removed", "kind", string(kind), "count", removed)
	}
}

var legacyBackupTempRe = regexp.MustCompile(`^wg-monitor-backup\.\d+$`)

// removeStaleBackupTemps убирает то, что бросил убитый прогон (нехватка
// памяти, перезагрузка): временные каталоги с копией базы и недописанные
// архивы старше backupStaleAfter. Свежие не трогает -- это может быть
// соседний живой прогон. Заодно убирает каталоги прежних версий, которые
// создавались рядом с --out-dir (wg-monitor-backup.<цифры>).
func removeStaleBackupTemps(outDir string, now time.Time) {
	stale := func(path string) bool {
		info, err := os.Lstat(path)
		return err == nil && now.Sub(info.ModTime()) > backupStaleAfter
	}
	if entries, err := os.ReadDir(outDir); err == nil {
		for _, entry := range entries {
			name, path := entry.Name(), filepath.Join(outDir, entry.Name())
			switch {
			case entry.IsDir() && (strings.HasPrefix(name, backupTempPrefix) || strings.HasPrefix(name, verifyTempPrefix)):
			case entry.Type().IsRegular() && strings.HasSuffix(name, backupPartialSuffix):
				if _, _, ok := backup.ParseArchiveName(strings.TrimSuffix(name, backupPartialSuffix)); !ok {
					continue
				}
			default:
				continue
			}
			if stale(path) {
				_ = os.RemoveAll(path)
			}
		}
	}
	parent := filepath.Dir(filepath.Clean(outDir))
	if entries, err := os.ReadDir(parent); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() || !legacyBackupTempRe.MatchString(entry.Name()) {
				continue
			}
			if path := filepath.Join(parent, entry.Name()); stale(path) {
				_ = os.RemoveAll(path)
			}
		}
	}
}

func resolveLayoutPath(path, root string) string {
	if root == "" || path == "" {
		return path
	}
	switch {
	case path == "/data":
		return filepath.Join(root, "data")
	case strings.HasPrefix(path, "/data/"):
		return filepath.Join(root, "data", strings.TrimPrefix(path, "/data/"))
	case path == "/secrets":
		return filepath.Join(root, "secrets")
	case strings.HasPrefix(path, "/secrets/"):
		return filepath.Join(root, "secrets", strings.TrimPrefix(path, "/secrets/"))
	case path == "/config":
		return filepath.Join(root, "config")
	case strings.HasPrefix(path, "/config/"):
		return filepath.Join(root, "config", strings.TrimPrefix(path, "/config/"))
	default:
		return path
	}
}

func loadBackupConfig(path string) (*backend.Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg backend.Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("db_path is required")
	}
	if cfg.Telegram.BotTokenFile == "" {
		return nil, fmt.Errorf("telegram.bot_token_file is required")
	}
	if cfg.Telegram.ChatID == 0 && cfg.Telegram.AdminUserID == 0 {
		return nil, fmt.Errorf("telegram chat_id/admin_user_id is required")
	}
	// Те же пути хранилищ, что видит работающий бэкенд.
	backend.ApplyStoreDefaults(&cfg)
	return &cfg, nil
}

func writeAgentsCSV(dbPath, dst string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT nickname, COALESCE(kind, ''), COALESCE(last_seen_at, ''), COALESCE(last_deployed_version, '') FROM users ORDER BY nickname`)
	if err != nil {
		return fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("nickname,kind,last_seen_at,last_deployed_version\n")
	for rows.Next() {
		var nick, kind, seen, version string
		if err := rows.Scan(&nick, &kind, &seen, &version); err != nil {
			return err
		}
		b.WriteString(csvCell(nick) + "," + csvCell(kind) + "," + csvCell(seen) + "," + csvCell(version) + "\n")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(b.String()), 0o600)
}

func csvCell(s string) string {
	if !strings.ContainsAny(s, "\",\r\n") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func readTrimmedFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func sendTelegramDocument(ctx context.Context, token string, chatID int64, path, caption string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if caption != "" {
		if err := mw.WriteField("caption", caption); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/sendDocument", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%s", redactTelegramBotToken(err.Error(), token))
	}
	defer resp.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(errBody))
		if msg == "" {
			return fmt.Errorf("telegram sendDocument HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("telegram sendDocument HTTP %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func redactTelegramBotToken(msg, token string) string {
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, "bot"+token, "bot<redacted>")
}
