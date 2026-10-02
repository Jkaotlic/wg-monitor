package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

const (
	fixtureBotToken   = "123456:ABC-very-secret-bot-token"
	fixturePassphrase = "fixture backup passphrase 9f2c"
	fixtureWizard     = "wizard-secret-token"
	fixtureDashboard  = "dashboard-secret-token"
)

// backupFixture -- живая система в миниатюре: база настоящими миграциями
// (открыта, как у работающего бэкенда), токены, хранилища, ключ оживления
// и сохранённый им пароль роутера.
type backupFixture struct {
	dir, dbPath, cfgPath, passPath, outDir, keyPath, botPath string
	live                                                     *db.DB
	now                                                      time.Time
}

func newBackupFixture(t *testing.T) *backupFixture {
	t.Helper()
	dir := t.TempDir()
	f := &backupFixture{
		dir:      dir,
		dbPath:   filepath.Join(dir, "data", "state.db"),
		cfgPath:  filepath.Join(dir, "backend.yaml"),
		passPath: filepath.Join(dir, "backup-passphrase.txt"),
		outDir:   filepath.Join(dir, "data", "backups"),
		keyPath:  filepath.Join(dir, "revive.key"),
		botPath:  filepath.Join(dir, "bot-token.txt"),
		now:      time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC),
	}
	if err := os.MkdirAll(filepath.Dir(f.dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	live, err := db.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Close() })
	f.live = live
	f.exec(t, `INSERT INTO users (nickname, token_hash, expected_exit_ip, awg_iface, telegram_user_id) VALUES
		('alpha', 'h1', '198.51.100.1', 'nwg0', 1001), ('beta', 'h2', '198.51.100.2', 'nwg0', 1001), ('gamma', 'h3', '198.51.100.3', 'nwg0', 1002)`)
	f.exec(t, `INSERT INTO router_operators (user_id, telegram_user_id, granted_by) VALUES (1, 2001, 1001), (2, 2002, 1001)`)
	for i := 0; i < 40; i++ {
		f.exec(t, `INSERT INTO events (user_id, check_name, status, ts) VALUES (1, 'ping', 'ok', ?)`, fmt.Sprintf("2026-10-06 00:%02d:00", i))
	}

	key := bytes.Repeat([]byte{0x5a}, revive.KeySize)
	mustWrite(t, f.keyPath, base64.StdEncoding.EncodeToString(key)+"\n")
	f.saveCredential(t, key, 1)

	mustWrite(t, f.botPath, fixtureBotToken+"\n")
	mustWrite(t, filepath.Join(dir, "wizard-token.txt"), fixtureWizard+"\n")
	mustWrite(t, filepath.Join(dir, "dashboard-token.txt"), fixtureDashboard+"\n")
	mustWrite(t, f.passPath, fixturePassphrase+"\n")
	mustWrite(t, filepath.Join(dir, "data", "amnezia-premium.json"), `{"keys":["vpn://PREMIUM-KEY"]}`)
	mustWrite(t, filepath.Join(dir, "data", "amnezia-selfhosted.json"), `{"instances":[{"ssh_host":"203.0.113.7"}]}`)
	mustWrite(t, filepath.Join(dir, "data", "awg3-panels.json"), `{"panels":[{"url":"https://panel.example.com"}]}`)
	mustWrite(t, filepath.Join(dir, "data", "hidemyname.json"), `{"codes":["1234567890"]}`)
	f.writeConfig(t, true)
	return f
}

func (f *backupFixture) writeConfig(t *testing.T, withReviveKey bool) {
	t.Helper()
	cfg := "db_path: " + slash(f.dbPath) + "\n" +
		"telegram:\n  bot_token_file: " + slash(f.botPath) + "\n  admin_user_id: 42\n" +
		"wizard:\n  token_file: " + slash(filepath.Join(f.dir, "wizard-token.txt")) + "\n" +
		"dashboard:\n  enabled: true\n  token_file: " + slash(filepath.Join(f.dir, "dashboard-token.txt")) + "\n"
	if withReviveKey {
		cfg += "revive:\n  key_file: " + slash(f.keyPath) + "\n"
	}
	mustWrite(t, f.cfgPath, cfg)
}

func (f *backupFixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.live.SQL().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func (f *backupFixture) saveCredential(t *testing.T, key []byte, routerID int64) {
	t.Helper()
	box, err := revive.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(routerID, revive.NewSecrets("root-password-fixture", "", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT OR REPLACE INTO router_credentials (user_id, nonce, ciphertext, saved_at) VALUES (?, ?, ?, '2026-09-18 10:00:00')`, routerID, nonce, ct)
}

func (f *backupFixture) opts(kind string) backupCommandOptions {
	return backupCommandOptions{
		ConfigPath:     f.cfgPath,
		PassphraseFile: f.passPath,
		OutDir:         f.outDir,
		Kind:           kind,
		LimitBytes:     47185920,
		TestKDF:        true,
		KeepDaily:      -1, KeepWeekly: -1,
		SmallKeepDaily: -1, SmallKeepWeekly: -1,
		FullKeepDaily: -1, FullKeepWeekly: -1,
		now:    func() time.Time { return f.now },
		stdout: &bytes.Buffer{},
		sendDocument: func(context.Context, string, int64, string, string) error {
			return errors.New("тест не ждал отправки в Telegram")
		},
		runCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("тест не ждал запуска внешней команды")
		},
	}
}

func (f *backupFixture) status(t *testing.T) backup.Status {
	t.Helper()
	s, err := backup.LoadStatus(filepath.Join(filepath.Dir(f.dbPath), "backup-status.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *backupFixture) outNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.outDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// decryptArchive -- открытый tar.gz архива из каталога бэкапов.
func (f *backupFixture) decryptArchive(t *testing.T, name string) []byte {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(f.outDir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(blob, []byte("WGMONBACKUP2\n")) {
		t.Fatalf("архив %s не в формате v2: %q", name, blob[:13])
	}
	for _, secret := range []string{fixtureBotToken, fixtureWizard, fixtureDashboard, fixturePassphrase, "PREMIUM-KEY", "root-password-fixture"} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Fatalf("в шифртексте %s открытый текст %q", name, secret)
		}
	}
	plain, err := backup.Decrypt(blob, []byte(fixturePassphrase))
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func archivedCount(t *testing.T, plainTGZ []byte, table string) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	mustWrite(t, path, string(tarMember(t, plainTGZ, "state.db")))
	d, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const (
	fixtureSmallName = "wg-monitor-small-backup-20261007T020000Z.tgz.enc"
	fixtureFullName  = "wg-monitor-full-backup-20261007T020000Z.tgz.enc"
)

func TestBackupBothWritesSmallAndFullWithStoresAndReviveKey(t *testing.T) {
	f := newBackupFixture(t)
	if err := runBackup(context.Background(), f.opts("both")); err != nil {
		t.Fatal(err)
	}
	if got := f.outNames(t); !slices.Equal(got, []string{fixtureFullName, fixtureSmallName}) {
		t.Fatalf("в каталоге бэкапов: %v", got)
	}
	wantMembers := []string{
		"state.db", "backend.yaml", "bot-token.txt", "wizard-token.txt", "dashboard-token.txt", "agents.csv", "manifest.txt",
		"amnezia-premium.json", "amnezia-selfhosted.json", "awg3-panels.json", "hidemyname.json", "revive.key",
	}
	for kind, name := range map[string]string{"small": fixtureSmallName, "full": fixtureFullName} {
		plain := f.decryptArchive(t, name)
		members := tarMembers(t, plain)
		for _, want := range wantMembers {
			if !members[want] {
				t.Errorf("%s: в архиве нет %s; есть %v", kind, want, members)
			}
		}
		if len(members) != len(wantMembers) {
			t.Errorf("%s: лишние файлы в архиве: %v", kind, members)
		}
		for n, mode := range tarModes(t, plain) {
			if mode != 0o600 {
				t.Errorf("%s: права %s = %o", kind, n, mode)
			}
		}
		wantKey, _ := os.ReadFile(f.keyPath)
		if !bytes.Equal(tarMember(t, plain, "revive.key"), wantKey) {
			t.Errorf("%s: revive.key в архиве не тот", kind)
		}
		manifest := string(tarMember(t, plain, "manifest.txt"))
		for _, want := range []string{
			"name=wg-monitor-" + kind + "-backup\n", "kind=" + kind + "\n", "format=encrypted-" + kind + "-v2\n",
			"created_utc=20261007T020000Z\n", "revive_key=yes\n",
			"stores=amnezia-premium.json,amnezia-selfhosted.json,awg3-panels.json,hidemyname.json\n",
		} {
			if !strings.Contains(manifest, want) {
				t.Errorf("%s: в манифесте нет %q:\n%s", kind, want, manifest)
			}
		}
		if archivedCount(t, plain, "users") != 3 || archivedCount(t, plain, "router_operators") != 2 || archivedCount(t, plain, "router_credentials") != 1 {
			t.Errorf("%s: роутеры, операторы или сохранённые пароли не перенесены", kind)
		}
		wantEvents := map[string]int{"small": 0, "full": 40}[kind]
		if got := archivedCount(t, plain, "events"); got != wantEvents {
			t.Errorf("%s: событий в архиве %d, ждали %d", kind, got, wantEvents)
		}
		if kind == "small" && !strings.Contains(manifest, "skipped_tables=events,daily_soft_flaps,awgm_ping_runs,alert_messages\n") {
			t.Errorf("small: манифест не называет пропущенные таблицы:\n%s", manifest)
		}
		if !strings.Contains(string(tarMember(t, plain, "agents.csv")), "alpha") {
			t.Errorf("%s: agents.csv без роутеров", kind)
		}
	}

	s := f.status(t)
	at := "2026-10-07T02:00:00Z"
	smallInfo, _ := os.Stat(filepath.Join(f.outDir, fixtureSmallName))
	fullInfo, _ := os.Stat(filepath.Join(f.outDir, fixtureFullName))
	if want := (backup.KindStatus{LastOKAt: at, LastRunAt: at, OK: true, SizeBytes: smallInfo.Size(), File: fixtureSmallName, Telegram: "off"}); s.Small != want {
		t.Errorf("состояние малого: %+v\nждали %+v", s.Small, want)
	}
	if want := (backup.KindStatus{LastOKAt: at, LastRunAt: at, OK: true, SizeBytes: fullInfo.Size(), File: fixtureFullName, Telegram: "off", Offsite: "off"}); s.Full != want {
		t.Errorf("состояние полного: %+v\nждали %+v", s.Full, want)
	}
	if s.Version != 1 || s.Verify.LastRunAt != "" {
		t.Errorf("состояние: %+v", s)
	}
	if smallInfo.Mode().Perm() != 0o600 || fullInfo.Mode().Perm() != 0o600 {
		t.Errorf("права архивов: %o %o", smallInfo.Mode().Perm(), fullInfo.Mode().Perm())
	}
}

func TestBackupKindSelectsArchivesAndKeepsOtherStatusSections(t *testing.T) {
	f := newBackupFixture(t)
	if err := runBackup(context.Background(), f.opts("full")); err != nil {
		t.Fatal(err)
	}
	if got := f.outNames(t); !slices.Equal(got, []string{fixtureFullName}) {
		t.Fatalf("--kind full: %v", got)
	}
	fullBefore := f.status(t).Full
	if !fullBefore.OK || f.status(t).Small.LastRunAt != "" {
		t.Fatalf("состояние после --kind full: %+v", f.status(t))
	}
	f.now = f.now.Add(24 * time.Hour)
	if err := runBackup(context.Background(), f.opts("small")); err != nil {
		t.Fatal(err)
	}
	if got := f.outNames(t); !slices.Equal(got, []string{fixtureFullName, "wg-monitor-small-backup-20261008T020000Z.tgz.enc"}) {
		t.Fatalf("--kind small: %v", got)
	}
	s := f.status(t)
	if s.Full != fullBefore {
		t.Fatalf("секция полного изменилась прогоном малого: %+v", s.Full)
	}
	if !s.Small.OK || s.Small.LastOKAt != "2026-10-08T02:00:00Z" {
		t.Fatalf("секция малого: %+v", s.Small)
	}

	bad := f.opts("tiny")
	if err := runBackup(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "--kind") {
		t.Fatalf("неизвестный вид: %v", err)
	}
	if err := runBackupCommand([]string{"--config", f.cfgPath, "--passphrase-file", f.passPath, "--out-dir", f.outDir, "stray"}); err == nil {
		t.Fatal("лишний аргумент принят")
	}
}

func TestBackupSendsOnlySmallToTelegram(t *testing.T) {
	f := newBackupFixture(t)
	opts := f.opts("both")
	opts.SendTelegram = true
	type sent struct {
		token, path, caption string
		chatID               int64
	}
	var calls []sent
	opts.sendDocument = func(_ context.Context, token string, chatID int64, path, caption string) error {
		calls = append(calls, sent{token, path, caption, chatID})
		return nil
	}
	if err := runBackup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("отправок в Telegram: %d, ждали одну (только малый)", len(calls))
	}
	c := calls[0]
	if c.token != fixtureBotToken || c.chatID != 42 || c.path != filepath.Join(f.outDir, fixtureSmallName) {
		t.Fatalf("отправка: %+v", c)
	}
	if !strings.Contains(c.caption, "Малый бэкап") || strings.Contains(c.caption, fixtureBotToken) {
		t.Fatalf("подпись: %q", c.caption)
	}
	s := f.status(t)
	if s.Small.Telegram != "ok" || !s.Small.OK || s.Full.Telegram != "off" || !s.Full.OK {
		t.Fatalf("состояние: %+v", s)
	}
}

func TestBackupSmallOverTelegramLimitIsRunErrorButFullStillMade(t *testing.T) {
	f := newBackupFixture(t)
	// Вчера всё было хорошо.
	ok := f.opts("small")
	ok.SendTelegram = true
	ok.sendDocument = func(context.Context, string, int64, string, string) error { return nil }
	if err := runBackup(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(24 * time.Hour)

	opts := f.opts("both")
	opts.SendTelegram = true
	opts.LimitBytes = 100
	opts.sendDocument = func(context.Context, string, int64, string, string) error {
		t.Error("архив больше лимита не должен отправляться")
		return nil
	}
	err := runBackup(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "small backup") || !strings.Contains(err.Error(), "Telegram") {
		t.Fatalf("ждали ошибку малого про Telegram, получили %v", err)
	}
	s := f.status(t)
	if s.Small.OK || s.Small.Telegram != "error" || !strings.Contains(s.Small.Error, "Telegram принимает до") {
		t.Fatalf("состояние малого: %+v", s.Small)
	}
	if s.Small.LastOKAt != "2026-10-07T02:00:00Z" || s.Small.LastRunAt != "2026-10-08T02:00:00Z" {
		t.Fatalf("last_ok_at должен остаться вчерашним: %+v", s.Small)
	}
	// Архив на диске есть, хоть и не ушёл; полный сделан.
	if s.Small.File != "wg-monitor-small-backup-20261008T020000Z.tgz.enc" || s.Small.SizeBytes == 0 {
		t.Fatalf("малый архив должен быть записан: %+v", s.Small)
	}
	if !s.Full.OK || s.Full.File != "wg-monitor-full-backup-20261008T020000Z.tgz.enc" {
		t.Fatalf("полный архив должен быть сделан несмотря на провал малого: %+v", s.Full)
	}
}

func TestBackupErrorsNeverLeakSecrets(t *testing.T) {
	f := newBackupFixture(t)
	opts := f.opts("both")
	opts.SendTelegram = true
	opts.OffsiteSCP = "backup@198.51.100.20:/srv/backups/"
	opts.OffsiteKey = filepath.Join(f.dir, "offsite_key")
	opts.sendDocument = func(context.Context, string, int64, string, string) error {
		return fmt.Errorf(`Post "https://api.telegram.org/bot%s/sendDocument": dial tcp: timeout; token=%s pass=%s`, fixtureBotToken, fixtureBotToken, fixturePassphrase)
	}
	opts.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Warning: something\nssh: connect to host 198.51.100.20 port 22: Connection timed out; " + fixturePassphrase + "\n"), errors.New("exit status 255")
	}
	err := runBackup(context.Background(), opts)
	if err == nil {
		t.Fatal("ждали ошибку")
	}
	raw, rerr := os.ReadFile(filepath.Join(filepath.Dir(f.dbPath), "backup-status.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, text := range []string{err.Error(), string(raw)} {
		for _, secret := range []string{fixtureBotToken, fixturePassphrase, fixtureWizard, fixtureDashboard} {
			if strings.Contains(text, secret) {
				t.Fatalf("секрет %q утёк в текст:\n%s", secret, text)
			}
		}
	}
	s := f.status(t)
	if s.Small.OK || s.Small.Telegram != "error" || !strings.Contains(s.Small.Error, "bot<redacted>/sendDocument") {
		t.Fatalf("состояние малого: %+v", s.Small)
	}
	if s.Full.OK || s.Full.Offsite != "error" || !strings.Contains(s.Full.Error, "Connection timed out") {
		t.Fatalf("состояние полного: %+v", s.Full)
	}
	// Оба архива при этом записаны: провал доставки -- не провал записи.
	if got := f.outNames(t); !slices.Equal(got, []string{fixtureFullName, fixtureSmallName}) {
		t.Fatalf("в каталоге бэкапов: %v", got)
	}
}

func TestBackupOffsiteCopiesOnlyFullWithExactScpCommand(t *testing.T) {
	f := newBackupFixture(t)
	opts := f.opts("both")
	opts.OffsiteSCP = "backup@198.51.100.20:/srv/backups/"
	opts.OffsiteKey = "/etc/wg-monitor/offsite_ed25519"
	var calls [][]string
	opts.runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	}
	if err := runBackup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	want := []string{"scp", "-B", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=20",
		"-i", "/etc/wg-monitor/offsite_ed25519", filepath.Join(f.outDir, fixtureFullName), "backup@198.51.100.20:/srv/backups/"}
	if len(calls) != 1 || !slices.Equal(calls[0], want) {
		t.Fatalf("команды: %v\nждали одну: %v", calls, want)
	}
	if s := f.status(t); s.Full.Offsite != "ok" || !s.Full.OK || s.Small.Offsite != "" {
		t.Fatalf("состояние: %+v", s)
	}
}

func TestBackupOffsiteMisconfigurationFailsFullWithoutRunningScp(t *testing.T) {
	for name, tc := range map[string]struct{ target, key string }{
		"нет ключа":          {"backup@198.51.100.20:/srv/", ""},
		"нет пути":           {"backup@198.51.100.20", "/k"},
		"нет пользователя":   {"server.example.com:/srv/", "/k"},
		"ключ scp в цели":    {"-oProxyCommand=x@h:/p", "/k"},
		"пробел в цели":      {"backup@198.51.100.20:/srv/a b", "/k"},
		"ключ начинается с-": {"backup@198.51.100.20:/srv/", "-oProxyCommand=x"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBackupFixture(t)
			opts := f.opts("both")
			opts.OffsiteSCP, opts.OffsiteKey = tc.target, tc.key
			opts.runCommand = func(context.Context, string, ...string) ([]byte, error) {
				t.Error("scp не должен запускаться")
				return nil, nil
			}
			if err := runBackup(context.Background(), opts); err == nil {
				t.Fatal("ждали ошибку")
			}
			s := f.status(t)
			if s.Full.OK || s.Full.Offsite != "error" || s.Full.File != fixtureFullName {
				t.Fatalf("состояние полного: %+v", s.Full)
			}
			if !s.Small.OK {
				t.Fatalf("малый не должен пострадать: %+v", s.Small)
			}
		})
	}
}

func TestBackupFailureBeforeWriteRecordsBothKindsAndLeavesNoDebris(t *testing.T) {
	f := newBackupFixture(t)
	// Старые архивы: при провале записи хранение их не трогает.
	old := []string{}
	for i := 1; i <= 12; i++ {
		at := f.now.AddDate(0, 0, -i)
		old = append(old, backup.ArchiveName(backup.KindFull, at), backup.ArchiveName(backup.KindSmall, at))
	}
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range old {
		mustWrite(t, filepath.Join(f.outDir, n), "old")
	}
	slices.Sort(old)
	if err := os.Remove(filepath.Join(f.dir, "wizard-token.txt")); err != nil {
		t.Fatal(err)
	}
	err := runBackup(context.Background(), f.opts("both"))
	if err == nil || !strings.Contains(err.Error(), "small backup") || !strings.Contains(err.Error(), "full backup") {
		t.Fatalf("ждали ошибки обоих видов, получили %v", err)
	}
	if got := f.outNames(t); !slices.Equal(got, old) {
		t.Fatalf("после провала в каталоге должно остаться только старое:\n%v", got)
	}
	s := f.status(t)
	for kind, sec := range map[string]backup.KindStatus{"small": s.Small, "full": s.Full} {
		if sec.OK || sec.Error == "" || sec.File != "" || sec.SizeBytes != 0 || sec.LastOKAt != "" || sec.LastRunAt == "" {
			t.Errorf("состояние %s: %+v", kind, sec)
		}
	}

	// Нет парольной фразы -- тоже провал обоих видов с записью состояния.
	f2 := newBackupFixture(t)
	if err := os.Remove(f2.passPath); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(context.Background(), f2.opts("both")); err == nil {
		t.Fatal("без парольной фразы прогон прошёл")
	}
	if s := f2.status(t); s.Small.OK || s.Full.OK || s.Small.Error == "" || s.Full.Error == "" {
		t.Fatalf("состояние без парольной фразы: %+v", s)
	}
}

func TestBackupAppliesRetentionPerKindAfterSuccessfulWrite(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 40; i++ {
		at := f.now.AddDate(0, 0, -i)
		mustWrite(t, filepath.Join(f.outDir, backup.ArchiveName(backup.KindFull, at)), "old")
		mustWrite(t, filepath.Join(f.outDir, backup.ArchiveName(backup.KindSmall, at)), "old")
	}
	foreign := []string{"notes.txt", "wg-monitor-full-backup-20250101T000000Z.tgz", "wg-monitor-small-backup-20250101T000000Z.tgz.enc.bak"}
	for _, n := range foreign {
		mustWrite(t, filepath.Join(f.outDir, n), "чужое")
	}
	if err := os.Mkdir(filepath.Join(f.outDir, "wg-monitor-small-backup-20250101T000000Z.tgz.enc"), 0o700); err != nil {
		t.Fatal(err) // каталог с именем архива -- тоже чужое
	}
	if err := runBackup(context.Background(), f.opts("both")); err != nil {
		t.Fatal(err)
	}
	// now = среда 07.10.2026. Малый 7+4, полный 3+0.
	want := append([]string{}, foreign...)
	want = append(want, "wg-monitor-small-backup-20250101T000000Z.tgz.enc")
	for _, stamp := range []string{"20261007", "20261006", "20261005"} {
		want = append(want, "wg-monitor-full-backup-"+stamp+"T020000Z.tgz.enc")
	}
	for _, stamp := range []string{"20261007", "20261006", "20261005", "20261004", "20261003", "20261002", "20261001", "20260927", "20260920", "20260913"} {
		want = append(want, "wg-monitor-small-backup-"+stamp+"T020000Z.tgz.enc")
	}
	slices.Sort(want)
	if got := f.outNames(t); !slices.Equal(got, want) {
		t.Fatalf("после чистки:\n%v\nждали:\n%v", got, want)
	}
}

func TestBackupRetentionFlagsPrecedence(t *testing.T) {
	unset := backupCommandOptions{KeepDaily: -1, KeepWeekly: -1, SmallKeepDaily: -1, SmallKeepWeekly: -1, FullKeepDaily: -1, FullKeepWeekly: -1}
	if got := unset.retentionFor(backup.KindSmall); got != (backup.Retention{KeepDaily: 7, KeepWeekly: 4}) {
		t.Fatalf("умолчание малого: %+v", got)
	}
	if got := unset.retentionFor(backup.KindFull); got != (backup.Retention{KeepDaily: 3, KeepWeekly: 0}) {
		t.Fatalf("умолчание полного: %+v", got)
	}
	common := unset
	common.KeepDaily, common.KeepWeekly = 10, 2
	if got := common.retentionFor(backup.KindFull); got != (backup.Retention{KeepDaily: 10, KeepWeekly: 2}) {
		t.Fatalf("общие флаги для полного: %+v", got)
	}
	specific := common
	specific.FullKeepDaily, specific.SmallKeepWeekly = 1, 0
	if got := specific.retentionFor(backup.KindFull); got != (backup.Retention{KeepDaily: 1, KeepWeekly: 2}) {
		t.Fatalf("свой флаг полного сильнее общего: %+v", got)
	}
	if got := specific.retentionFor(backup.KindSmall); got != (backup.Retention{KeepDaily: 10, KeepWeekly: 0}) {
		t.Fatalf("свой флаг малого сильнее общего: %+v", got)
	}
}

func TestBackupRemovesStaleTempsOfKilledRuns(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(f.outDir)
	old := time.Now().Add(-7 * time.Hour)
	mk := func(path string, dir bool, stale bool) {
		t.Helper()
		if dir {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(path, "state.db"), "копия базы убитого прогона")
		} else {
			mustWrite(t, path, "x")
		}
		if stale {
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	staleTmp := filepath.Join(f.outDir, ".tmp-wg-monitor-backup.111")
	staleVerify := filepath.Join(f.outDir, ".tmp-wg-monitor-verify.222")
	freshTmp := filepath.Join(f.outDir, ".tmp-wg-monitor-backup.333")
	stalePartial := filepath.Join(f.outDir, "wg-monitor-full-backup-20261001T020000Z.tgz.enc.partial")
	foreignPartial := filepath.Join(f.outDir, "movie.mkv.partial")
	legacy := filepath.Join(parent, "wg-monitor-backup.4242")
	legacyFresh := filepath.Join(parent, "wg-monitor-backup.4343")
	lookalike := filepath.Join(parent, "wg-monitor-backup.keep")
	mk(staleTmp, true, true)
	mk(staleVerify, true, true)
	mk(freshTmp, true, false)
	mk(stalePartial, false, true)
	mk(foreignPartial, false, true)
	mk(legacy, true, true)
	mk(legacyFresh, true, false)
	mk(lookalike, true, true)

	opts := f.opts("small")
	opts.now = time.Now // возраст брошенного считается от настоящих часов
	if err := runBackup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{staleTmp, staleVerify, stalePartial, legacy} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("брошенное не убрано: %s", gone)
		}
	}
	for _, kept := range []string{freshTmp, foreignPartial, legacyFresh, lookalike} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("убрано лишнее: %s", kept)
		}
	}
}

// Полный архив пишется потоком: память не растёт с размером базы. 96 МБ
// через tar+gzip+шифр -- суммарные выделения остаются в единицах МБ.
func TestWriteEncryptedArchiveMemoryIsBounded(t *testing.T) {
	const size = 96 << 20
	const allocLimit = 24 << 20
	dir := t.TempDir()
	big := filepath.Join(dir, "state.db")
	fh, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(size); err != nil {
		t.Fatal(err)
	}
	fh.Close()
	dst := filepath.Join(dir, "out.tgz.enc")

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := writeEncryptedArchive(context.Background(), dst, []archiveMember{{"state.db", big}}, []byte(fixturePassphrase), backup.TestParams()); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("архив из файла %d МБ: всего выделено %.2f МБ", size>>20, float64(allocated)/(1<<20))
	if allocated > allocLimit {
		t.Fatalf("за запись архива выделено %d байт, предел %d -- архив собирается в памяти?", allocated, allocLimit)
	}

	// И читается потоком обратно.
	out := filepath.Join(dir, "out")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&before)
	names, err := extractEncryptedArchive(context.Background(), dst, []byte(fixturePassphrase), out, 0)
	if err != nil || !slices.Equal(names, []string{"state.db"}) {
		t.Fatalf("распаковка: %v %v", names, err)
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > allocLimit {
		t.Fatalf("за чтение архива выделено %d байт, предел %d", allocated, allocLimit)
	}
	if info, err := os.Stat(filepath.Join(out, "state.db")); err != nil || info.Size() != size {
		t.Fatalf("распакованный файл: %v %v", info, err)
	}
}

func TestWriteEncryptedArchiveRefusesExistingFileAndCancel(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	mustWrite(t, src, "payload")
	dst := filepath.Join(dir, "out.tgz.enc")
	mustWrite(t, dst, "чужой файл")
	if err := writeEncryptedArchive(context.Background(), dst, []archiveMember{{"a.txt", src}}, []byte("p"), backup.TestParams()); err == nil {
		t.Fatal("существующий файл перезаписан")
	}
	if body, _ := os.ReadFile(dst); string(body) != "чужой файл" {
		t.Fatal("существующий файл испорчен")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeEncryptedArchive(ctx, filepath.Join(dir, "c.tgz.enc"), []archiveMember{{"a.txt", src}}, []byte("p"), backup.TestParams()); !errors.Is(err, context.Canceled) {
		t.Fatalf("отмена: %v", err)
	}
}
