package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

type mapKV map[string]string

func (m mapKV) Get(k string) (string, error) { return m[k], nil }
func (m mapKV) Set(k, v string) error        { m[k] = v; return nil }

type sentMsg struct {
	chat int64
	text string
}

type fakeAlertSender struct {
	msgs []sentMsg
	fail error
}

func (s *fakeAlertSender) SendMessage(_ context.Context, chatID int64, _ *int64, text, _ string, _ *int64) (int64, error) {
	if s.fail != nil {
		return 0, s.fail
	}
	s.msgs = append(s.msgs, sentMsg{chatID, text})
	return int64(len(s.msgs)), nil
}

const alertAdmin int64 = 4242

var alertT0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

type alertRig struct {
	t      *testing.T
	path   string
	kv     mapKV
	sender *fakeAlertSender
	clk    *fakeClock
	start  time.Time
}

func newAlertRig(t *testing.T) *alertRig {
	return &alertRig{t: t, path: filepath.Join(t.TempDir(), backup.StatusFileName), kv: mapKV{}, sender: &fakeAlertSender{},
		clk: &fakeClock{t: alertT0}, start: alertT0.Add(-48 * time.Hour)}
}

func (r *alertRig) alerter() *BackupAlerter {
	return NewBackupAlerter(BackupAlertConfig{StatusPath: r.path, KV: r.kv, Sender: r.sender, AdminUserID: alertAdmin, Now: r.clk.now, StartedAt: r.start})
}

func (r *alertRig) status(mutate func(*backup.Status)) {
	r.t.Helper()
	if err := backup.UpdateStatus(r.path, mutate); err != nil {
		r.t.Fatal(err)
	}
}

func ts(base time.Time, ago time.Duration) string { return base.Add(-ago).UTC().Format(time.RFC3339) }

func (r *alertRig) healthy() {
	r.status(func(s *backup.Status) {
		ok := backup.KindStatus{LastOKAt: ts(r.clk.t, 5*time.Hour), LastRunAt: ts(r.clk.t, 5*time.Hour), OK: true, Telegram: "ok"}
		s.Small, s.Full = ok, ok
		s.Verify = backup.VerifyStatus{LastRunAt: ts(r.clk.t, 50*time.Hour), OK: true}
	})
}

func (r *alertRig) texts() []string {
	var out []string
	for _, m := range r.sender.msgs {
		if m.chat != alertAdmin {
			r.t.Fatalf("тревога ушла не админу: %+v", m)
		}
		out = append(out, m.text)
	}
	return out
}

var (
	alertQuotedRe = regexp.MustCompile(`«[^»]*»`)
	alertLatinRe  = regexp.MustCompile(`[A-Za-z]{2,}`)
)

func latinOutsideGuillemets(text string) []string {
	return alertLatinRe.FindAllString(strings.ReplaceAll(alertQuotedRe.ReplaceAllString(text, "«»"), "VPN", ""), -1)
}

func TestBackupAlertStaleSmallFiresOnceAndRepeatsAfterDay(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	a := r.alerter()
	a.Tick(context.Background())
	if len(r.sender.msgs) != 0 {
		t.Fatalf("здоровый бэкап: %v", r.texts())
	}
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 30*time.Hour) })
	a.Tick(context.Background())
	if got := r.texts(); len(got) != 1 || !strings.Contains(got[0], "не делался") {
		t.Fatalf("устаревший малый: %v", got)
	}
	r.clk.advance(23*time.Hour + 59*time.Minute)
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(alertT0, 30*time.Hour) })
	a.Tick(context.Background())
	if len(r.sender.msgs) != 1 {
		t.Fatalf("повтор раньше суток: %v", r.texts())
	}
	r.clk.advance(2 * time.Minute)
	a.Tick(context.Background())
	if len(r.sender.msgs) != 2 {
		t.Fatalf("через сутки ждали повтор: %v", r.texts())
	}
}

func TestBackupAlertBoundaryOf26Hours(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 26*time.Hour) })
	r.alerter().Tick(context.Background())
	if len(r.sender.msgs) != 0 {
		t.Fatalf("ровно 26 часов -- ещё норма: %v", r.texts())
	}
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 26*time.Hour+time.Second) })
	r.alerter().Tick(context.Background())
	if len(r.sender.msgs) != 1 {
		t.Fatalf("26 часов и секунда: %v", r.texts())
	}
}

func TestBackupAlertFailedRunOfAnyKind(t *testing.T) {
	for name, mutate := range map[string]func(*backup.Status){
		"малый": func(s *backup.Status) {
			s.Small.OK, s.Small.Telegram, s.Small.Error = false, "error", "архив не ушёл в Telegram: он 50,0 МБ, а Telegram принимает до 45,0 МБ"
		},
		"полный": func(s *backup.Status) {
			s.Full.OK, s.Full.Offsite, s.Full.Error = false, "error", "архив не скопирован на внешний сервер: сервер недоступен"
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newAlertRig(t)
			r.healthy()
			r.status(mutate)
			r.alerter().Tick(context.Background())
			got := r.texts()
			if len(got) != 1 || !strings.Contains(strings.ToLower(got[0]), name) || !strings.Contains(got[0], "«") {
				t.Fatalf("%v", got)
			}
		})
	}
}

// Убитый на полпути прогон (отметка «не завершён») тревожит, когда прошло
// больше двух часов; идущий -- нет.
func TestBackupAlertUnfinishedRunNeedsTwoHours(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) {
		s.Full.OK, s.Full.Error, s.Full.LastRunAt = false, backup.RunUnfinishedText, ts(r.clk.t, 119*time.Minute)
	})
	a := r.alerter()
	a.Tick(context.Background())
	if len(r.sender.msgs) != 0 {
		t.Fatalf("идущий прогон: %v", r.texts())
	}
	r.clk.advance(2 * time.Minute)
	a.Tick(context.Background())
	if got := r.texts(); len(got) != 1 || !strings.Contains(got[0], "Полный") {
		t.Fatalf("убитый прогон: %v", got)
	}
}

func TestBackupAlertVerifyFailed(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) {
		s.Verify = backup.VerifyStatus{LastRunAt: ts(r.clk.t, time.Hour), OK: false, Error: "в архиве операторов 1, в манифесте архива 2"}
	})
	r.alerter().Tick(context.Background())
	if got := r.texts(); len(got) != 1 || !strings.Contains(got[0], "Проверка восстановления") {
		t.Fatalf("%v", got)
	}
}

func TestBackupAlertRecoveryMessageOnce(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) { s.Full.OK, s.Full.Error = false, "архив не записан: x" })
	a := r.alerter()
	a.Tick(context.Background())
	if len(r.sender.msgs) != 1 {
		t.Fatal("нет тревоги")
	}
	r.healthy()
	r.clk.advance(time.Hour)
	a.Tick(context.Background())
	got := r.texts()
	if len(got) != 2 || got[1] != "Бэкап снова в порядке." {
		t.Fatalf("восстановление: %v", got)
	}
	r.clk.advance(time.Hour)
	a.Tick(context.Background())
	if len(r.sender.msgs) != 2 {
		t.Fatalf("второе «в порядке»: %v", r.texts())
	}
	// Здоровье без предшествующей тревоги молчит.
	r2 := newAlertRig(t)
	r2.healthy()
	r2.alerter().Tick(context.Background())
	if len(r2.sender.msgs) != 0 {
		t.Fatalf("лишнее сообщение: %v", r2.texts())
	}
}

// Перезапуск бэкенда (новый экземпляр, то же хранилище) не повторяет
// тревогу и не теряет «снова в порядке».
func TestBackupAlertSurvivesRestart(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 40*time.Hour) })
	r.alerter().Tick(context.Background())
	r.clk.advance(30 * time.Minute)
	r.alerter().Tick(context.Background()) // «перезапущенный» экземпляр
	if len(r.sender.msgs) != 1 {
		t.Fatalf("после перезапуска повтор: %v", r.texts())
	}
	r.healthy()
	r.clk.advance(30 * time.Minute)
	r.alerter().Tick(context.Background())
	if got := r.texts(); len(got) != 2 || got[1] != "Бэкап снова в порядке." {
		t.Fatalf("восстановление после перезапуска: %v", got)
	}
}

// Правило «ещё ни разу не удался»: молчит, пока бэкенд работает меньше 26
// часов (свежая установка до первого ночного прогона), и тревожит после.
// Провал уже сделанного прогона тревожит сразу -- это отдельная причина.
func TestBackupAlertFreshInstallStaysQuietUntilUptime26h(t *testing.T) {
	r := newAlertRig(t)
	r.start = alertT0.Add(-time.Hour)
	a := r.alerter()
	a.Tick(context.Background()) // файла нет
	if len(r.sender.msgs) != 0 {
		t.Fatalf("свежая установка без файла: %v", r.texts())
	}
	// Идёт первый прогон: отметка «начат» -- файл есть, но это не провал.
	r.status(func(s *backup.Status) {
		s.Small = backup.KindStatus{LastRunAt: ts(alertT0, time.Minute), Error: backup.RunUnfinishedText}
	})
	a.Tick(context.Background())
	if len(r.sender.msgs) != 0 {
		t.Fatalf("первый прогон идёт: %v", r.texts())
	}
	// Бэкенд работает больше 26 часов, файла так и нет.
	r2 := newAlertRig(t)
	r2.start = alertT0.Add(-27 * time.Hour)
	r2.alerter().Tick(context.Background())
	if got := r2.texts(); len(got) != 1 || !strings.Contains(got[0], "ещё ни разу") {
		t.Fatalf("давно работает, бэкапа нет: %v", got)
	}
	// Битый файл ведёт себя как отсутствующий.
	r3 := newAlertRig(t)
	if err := writeAlertFile(r3.path, "{не json"); err != nil {
		t.Fatal(err)
	}
	r3.start = alertT0.Add(-time.Hour)
	r3.alerter().Tick(context.Background())
	if len(r3.sender.msgs) != 0 {
		t.Fatalf("битый файл на свежей установке: %v", r3.texts())
	}
}

func TestBackupAlertRetriesWhenSendFails(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 40*time.Hour) })
	r.sender.fail = errors.New("telegram недоступен")
	a := r.alerter()
	a.Tick(context.Background())
	r.sender.fail = nil
	r.clk.advance(time.Minute)
	a.Tick(context.Background())
	if len(r.sender.msgs) != 1 {
		t.Fatalf("после неудачной отправки ждали повтор на следующем тике: %v", r.texts())
	}
}

func TestBackupAlertNoAdminNoSend(t *testing.T) {
	r := newAlertRig(t)
	r.healthy()
	r.status(func(s *backup.Status) { s.Small.OK = false })
	a := NewBackupAlerter(BackupAlertConfig{StatusPath: r.path, KV: r.kv, Sender: r.sender, Now: r.clk.now, StartedAt: r.start})
	a.Tick(context.Background())
	if len(r.sender.msgs) != 0 {
		t.Fatal("без admin_user_id отправлять некому")
	}
}

// Тексты тревог -- по правилам «тревоги говорят с владельцем»: вне ёлочек
// нет латиницы, нет жаргона и путей.
func TestBackupAlertTextsSpeakHumanRussian(t *testing.T) {
	raws := []string{
		"архив не записан: open /var/lib/wg-monitor/backups/x.partial: permission denied",
		"архив не ушёл в Telegram: он 50,0 МБ, а Telegram принимает до 45,0 МБ",
		"архив не скопирован на внешний сервер: ключ сервера изменился",
		"read passphrase: open /etc/x: no such file",
		backup.RunUnfinishedText,
		"что-то новое",
	}
	var all []string
	for _, raw := range raws {
		r := newAlertRig(t)
		r.healthy()
		r.status(func(s *backup.Status) {
			s.Small.OK, s.Small.Error = false, raw
			s.Full.OK, s.Full.Error = false, raw
			s.Full.LastRunAt = ts(r.clk.t, 3*time.Hour)
			s.Small.LastRunAt = ts(r.clk.t, 3*time.Hour)
			s.Small.LastOKAt = ts(r.clk.t, 40*time.Hour)
			s.Verify = backup.VerifyStatus{LastRunAt: ts(r.clk.t, time.Hour), Error: raw}
		})
		r.alerter().Tick(context.Background())
		all = append(all, r.texts()...)
	}
	r := newAlertRig(t)
	r.start = alertT0.Add(-30 * time.Hour)
	r.alerter().Tick(context.Background())
	all = append(all, r.texts()...)
	all = append(all, backupRecoveredText)
	if len(all) < 8 {
		t.Fatalf("мало текстов для проверки: %d", len(all))
	}
	banned := []string{"scp", "ssh", "journalctl", "systemctl", "/var/", "/etc/", ".partial", "passphrase", "state.db", "OOM", "exit status"}
	for _, text := range all {
		if bad := latinOutsideGuillemets(text); len(bad) > 0 {
			t.Errorf("латиница вне ёлочек %v в тексте:\n%s", bad, text)
		}
		for _, b := range banned {
			if strings.Contains(text, b) {
				t.Errorf("жаргон %q в тексте:\n%s", b, text)
			}
		}
		if strings.Contains(text, "/") && !strings.Contains(text, "«") {
			t.Errorf("путь в тексте:\n%s", text)
		}
	}
}

// Состояние тревог хранится в настоящей таблице tg_state: миграция не нужна.
func TestBackupAlertStateInRealKV(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r := newAlertRig(t)
	r.kv = nil
	r.healthy()
	r.status(func(s *backup.Status) { s.Small.LastOKAt = ts(r.clk.t, 40*time.Hour) })
	mk := func() *BackupAlerter {
		return NewBackupAlerter(BackupAlertConfig{StatusPath: r.path, KV: d.KV(), Sender: r.sender, AdminUserID: alertAdmin, Now: r.clk.now, StartedAt: r.start})
	}
	mk().Tick(context.Background())
	r.clk.advance(time.Minute)
	mk().Tick(context.Background())
	if len(r.sender.msgs) != 1 {
		t.Fatalf("через настоящее хранилище: %v", r.texts())
	}
}

func writeAlertFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }
