package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

const unfinishedRunText = "прогон не завершён"

func (f *backupFixture) statusFilePath() string {
	return filepath.Join(filepath.Dir(f.dbPath), "backup-status.json")
}

// R4: перед работой каждого вида в файл состояния пишется отметка «начат»;
// убитый прогон не оставляет вчерашнюю зелёную картину.
func TestBackupWritesStartedMarkerBeforeWork(t *testing.T) {
	f := newBackupFixture(t)
	// Вчерашний удачный прогон: его last_ok_at сохраняется.
	yesterday := "2026-10-06T02:00:00Z"
	if err := backup.UpdateStatus(f.statusFilePath(), func(s *backup.Status) {
		ok := backup.KindStatus{LastOKAt: yesterday, LastRunAt: yesterday, OK: true, File: "old", SizeBytes: 5, Telegram: "ok"}
		s.Small, s.Full = ok, ok
	}); err != nil {
		t.Fatal(err)
	}
	opts := f.opts("both")
	opts.SendTelegram = true
	opts.OffsiteSCP = "backup@198.51.100.20:/srv/backups/"
	opts.OffsiteKey = "/etc/wg-monitor/offsite_ed25519"
	var seenSmall, seenFull backup.KindStatus
	opts.sendDocument = func(context.Context, string, int64, string, string) error {
		seenSmall = f.status(t).Small
		seenFull = f.status(t).Full // полный ещё не начат: вчерашний
		return nil
	}
	var fullMidRun backup.KindStatus
	opts.runCommand = func(context.Context, string, ...string) ([]byte, error) {
		fullMidRun = f.status(t).Full
		return nil, nil
	}
	if err := runBackup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]backup.KindStatus{"малый": seenSmall, "полный": fullMidRun} {
		if got.OK || got.Error != unfinishedRunText || got.LastRunAt != "2026-10-07T02:00:00Z" || got.LastOKAt != yesterday {
			t.Errorf("%s во время прогона: %+v", name, got)
		}
	}
	if !seenFull.OK || seenFull.LastRunAt != yesterday {
		t.Errorf("полный до своего начала тронут: %+v", seenFull)
	}
	if s := f.status(t); !s.Small.OK || !s.Full.OK || s.Small.Error != "" || s.Full.Error != "" {
		t.Fatalf("после прогона отметка должна быть перезаписана итогом: %+v", s)
	}
}

func TestBackupBadKindWritesStatusError(t *testing.T) {
	f := newBackupFixture(t)
	opts := f.opts("sometimes")
	if err := runBackup(context.Background(), opts); err == nil {
		t.Fatal("неверный --kind принят")
	}
	s := f.status(t)
	for name, sec := range map[string]backup.KindStatus{"small": s.Small, "full": s.Full} {
		if sec.OK || sec.LastRunAt != "2026-10-07T02:00:00Z" || !strings.Contains(sec.Error, "--kind") {
			t.Errorf("%s: %+v", name, sec)
		}
	}
}

func TestBackupBadConfigWritesStatusErrorWhenDBPathKnown(t *testing.T) {
	f := newBackupFixture(t)
	// Конфиг читается, но не годится (нет токена бота): db_path известен.
	mustWrite(t, f.cfgPath, "db_path: "+slash(f.dbPath)+"\n")
	err := runBackup(context.Background(), f.opts("small"))
	if err == nil {
		t.Fatal("плохой конфиг принят")
	}
	s := f.status(t)
	if s.Small.OK || s.Small.LastRunAt != "2026-10-07T02:00:00Z" || !strings.Contains(s.Small.Error, "конфигурац") {
		t.Fatalf("малый: %+v", s.Small)
	}
	if s.Full.LastRunAt != "" {
		t.Fatalf("полный не запрашивали, но он тронут: %+v", s.Full)
	}
	if strings.Contains(s.Small.Error, f.dir) {
		t.Fatalf("путь в тексте состояния: %q", s.Small.Error)
	}
	// Конфига нет совсем -- писать некуда, остаётся ошибка команды.
	f2 := newBackupFixture(t)
	if err := os.Remove(f2.cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(context.Background(), f2.opts("small")); err == nil {
		t.Fatal("нет конфига, а прогон прошёл")
	}
	if _, err := os.Stat(f2.statusFilePath()); err == nil {
		t.Fatal("файл состояния появился без известного db_path")
	}
}

// R6: поля доставки в состоянии честны и при провале до доставки.
func TestBackupDeliveryFieldsOnPreparationFailure(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.Remove(f.passPath); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(context.Background(), f.opts("both")); err == nil {
		t.Fatal("без парольной фразы прогон прошёл")
	}
	s := f.status(t)
	if s.Small.Telegram != "off" || s.Full.Telegram != "off" || s.Full.Offsite != "off" {
		t.Fatalf("ничего не настроено -- везде off: %+v", s)
	}

	f2 := newBackupFixture(t)
	if err := os.Remove(f2.passPath); err != nil {
		t.Fatal(err)
	}
	opts := f2.opts("both")
	opts.SendTelegram = true
	opts.OffsiteSCP, opts.OffsiteKey = "backup@198.51.100.20:/srv/", "/etc/wg-monitor/k"
	if err := runBackup(context.Background(), opts); err == nil {
		t.Fatal("без парольной фразы прогон прошёл")
	}
	s = f2.status(t)
	if s.Small.Telegram != "error" || s.Full.Telegram != "off" || s.Full.Offsite != "error" {
		t.Fatalf("доставка запрошена и не состоялась -- error: %+v", s)
	}

	// Провал сборки малого после запроса отправки -- тоже error, не off.
	f3 := newBackupFixture(t)
	if err := os.Remove(filepath.Join(f3.dir, "wizard-token.txt")); err != nil {
		t.Fatal(err)
	}
	opts = f3.opts("small")
	opts.SendTelegram = true
	if err := runBackup(context.Background(), opts); err == nil {
		t.Fatal("ждали ошибку")
	}
	if s := f3.status(t); s.Small.Telegram != "error" {
		t.Fatalf("telegram при провале сборки: %+v", s.Small)
	}
}

// R6: сырой вывод scp (хост, путь) в файл состояния не попадает: только
// нейтральная причина словами.
func TestBackupOffsiteReasonIsNeutralWords(t *testing.T) {
	cases := map[string]struct{ output, want string }{
		"сервер недоступен": {"ssh: connect to host 198.51.100.20 port 22: Connection timed out", "сервер недоступен"},
		"отказ в доступе":   {"backup@198.51.100.20: Permission denied (publickey).", "отказ в доступе"},
		"нет места":         {"scp: /srv/secret-dir/x.partial: No space left on device", "нет места"},
		"ключ сервера":      {"WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! Host key verification failed.", "ключ сервера изменился"},
		"прочее":            {"scp: something odd at /srv/secret-dir", "ошибка копирования"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBackupFixture(t)
			opts := f.opts("full")
			opts.OffsiteSCP, opts.OffsiteKey = "backup@198.51.100.20:/srv/secret-dir/", "/etc/wg-monitor/k"
			opts.runCommand = func(context.Context, string, ...string) ([]byte, error) {
				return []byte(tc.output + "\n"), errors.New("exit status 255")
			}
			err := runBackup(context.Background(), opts)
			if err == nil {
				t.Fatal("ждали ошибку")
			}
			s := f.status(t)
			if !strings.Contains(s.Full.Error, tc.want) {
				t.Fatalf("причина: %q, ждали %q", s.Full.Error, tc.want)
			}
			raw, _ := os.ReadFile(f.statusFilePath())
			for _, leak := range []string{"198.51.100.20", "secret-dir", "Permission denied", "port 22"} {
				if bytes.Contains(raw, []byte(leak)) {
					t.Fatalf("в файле состояния %q:\n%s", leak, raw)
				}
			}
		})
	}
}

// R6: чужой .partial (другой запуск ещё пишет) не удаляется.
func TestBackupDoesNotRemoveForeignPartial(t *testing.T) {
	f := newBackupFixture(t)
	if err := os.MkdirAll(f.outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(f.outDir, fixtureSmallName+".partial")
	mustWrite(t, partial, "чужая запись в процессе")
	if err := os.Chtimes(partial, f.now, f.now); err != nil { // свежий: уборка старого его не берёт
		t.Fatal(err)
	}
	if err := runBackup(context.Background(), f.opts("small")); err == nil {
		t.Fatal("ждали отказ: чужой .partial занят")
	}
	body, err := os.ReadFile(partial)
	if err != nil || string(body) != "чужая запись в процессе" {
		t.Fatalf("чужой .partial тронут: %q %v", body, err)
	}
}

// R3: запуск scp с запиненным known_hosts рядом с базой и без чужих ключей.
func TestBackupOffsiteScpPinsKnownHostsAndIdentity(t *testing.T) {
	f := newBackupFixture(t)
	opts := f.opts("full")
	opts.OffsiteSCP, opts.OffsiteKey = "backup@198.51.100.20:/srv/backups/", "/etc/wg-monitor/offsite_ed25519"
	var args []string
	opts.runCommand = func(_ context.Context, name string, a ...string) ([]byte, error) {
		args = append([]string{name}, a...)
		return nil, nil
	}
	if err := runBackup(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	want := []string{"scp", "-B",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(filepath.Dir(f.dbPath), "backup-known_hosts"),
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=20",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-i", "/etc/wg-monitor/offsite_ed25519",
		filepath.Join(f.outDir, fixtureFullName), "backup@198.51.100.20:/srv/backups/"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("scp:\n%v\nждали:\n%v", args, want)
	}
}

func TestBackupOffsiteTargetIsStrict(t *testing.T) {
	for _, bad := range []string{
		"backup@198.51.100.20:/srv/a;rm -rf /", "backup@198.51.100.20:/srv/$(id)", "backup@198.51.100.20:/srv/`id`",
		"backup@198.51.100.20:/srv/a b", "-oProxyCommand=x@h:/p", "backup@198.51.100.20:/srv/a'b", "backup@host:/srv/a|b",
		"backup@198.51.100.20", "host:/srv", "backup@:/srv", "backup@host:",
	} {
		if err := validateOffsiteTarget(bad); err == nil {
			t.Errorf("цель %q принята", bad)
		}
	}
	for _, good := range []string{"backup@198.51.100.20:/srv/backups/", "u@198.51.100.21:backups", "u-1@198.51.100.22:/srv/x_y.z~/+"} {
		if err := validateOffsiteTarget(good); err != nil {
			t.Errorf("цель %q отвергнута: %v", good, err)
		}
	}
}

// R2: проверка берёт архивы не позже «сейчас» и не соглашается на устаревший.
func TestBackupVerifyIgnoresFutureStampedArchive(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	// «Из будущего» (часы сбились или подложен): не выбирается.
	mustWrite(t, filepath.Join(f.outDir, "wg-monitor-small-backup-20271007T020000Z.tgz.enc"), "garbage")
	var out bytes.Buffer
	if err := runBackupVerify(context.Background(), f.verifyOpts(&out)); err != nil {
		t.Fatalf("архив из будущего подменил настоящий: %v", err)
	}
	if !strings.Contains(out.String(), fixtureSmallName) {
		t.Fatalf("проверен не тот архив:\n%s", out.String())
	}
}

func TestBackupVerifyFailsOnStaleNewestSmallArchive(t *testing.T) {
	f := newBackupFixture(t)
	f.backupSmall(t)
	opts := f.verifyOpts(&bytes.Buffer{})
	opts.now = func() time.Time { return f.now.Add(49 * time.Hour) }
	err := runBackupVerify(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "старше 48 часов") {
		t.Fatalf("устаревший архив: %v", err)
	}
	s := f.status(t)
	if s.Verify.OK || !strings.Contains(s.Verify.Error, "старше 48 часов") {
		t.Fatalf("состояние проверки: %+v", s.Verify)
	}
	// Ровно на границе -- ещё годится.
	opts.now = func() time.Time { return f.now.Add(47 * time.Hour) }
	if err := runBackupVerify(context.Background(), opts); err != nil {
		t.Fatalf("архив 47 часов: %v", err)
	}
}
