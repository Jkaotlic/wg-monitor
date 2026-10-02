package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func writeBackupStatus(t *testing.T, path string, mutate func(*backup.Status)) {
	t.Helper()
	if err := backup.UpdateStatus(path, mutate); err != nil {
		t.Fatal(err)
	}
}

func goodBackupStatus(s *backup.Status) {
	s.Small = backup.KindStatus{LastOKAt: "2026-10-07T02:00:05Z", LastRunAt: "2026-10-07T02:00:05Z", OK: true,
		SizeBytes: 4_000_000, File: "wg-monitor-small-backup-20261007T020000Z.tgz.enc", Telegram: "ok"}
	s.Full = backup.KindStatus{LastOKAt: "2026-10-07T02:03:00Z", LastRunAt: "2026-10-07T02:03:00Z", OK: true,
		SizeBytes: 190_000_000, File: "wg-monitor-full-backup-20261007T020000Z.tgz.enc", Telegram: "off", Offsite: "ok"}
	s.Verify = backup.VerifyStatus{LastRunAt: "2026-10-04T03:30:00Z", OK: true, Routers: 3}
}

func TestBackupStatusSourceUnknownWhenFileMissingOrGarbled(t *testing.T) {
	path := filepath.Join(t.TempDir(), backup.StatusFileName)
	clk := &fakeClock{t: time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)}
	src := NewBackupStatusSource(path, clk.now)
	if got := src.Summary(); got.Known || got.Small != nil || got.Full != nil || got.Verify != nil {
		t.Fatalf("нет файла: %+v", got)
	}
	clk.advance(time.Minute)
	if err := os.WriteFile(path, []byte("{не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := src.Summary(); got.Known {
		t.Fatalf("битый файл: %+v", got)
	}
	raw, err := json.Marshal(src.Summary())
	if err != nil || string(raw) != `{"known":false}` {
		t.Fatalf("неизвестное состояние в JSON: %s %v", raw, err)
	}
}

func TestBackupStatusSourceReadsAndCaches(t *testing.T) {
	path := filepath.Join(t.TempDir(), backup.StatusFileName)
	writeBackupStatus(t, path, goodBackupStatus)
	clk := &fakeClock{t: time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)}
	src := NewBackupStatusSource(path, clk.now)

	got := src.Summary()
	if !got.Known || got.Small == nil || got.Full == nil || got.Verify == nil {
		t.Fatalf("сводка: %+v", got)
	}
	if got.Small.LastOKAt != "2026-10-07T02:00:05Z" || !got.Small.OK || got.Small.SizeBytes != 4_000_000 || got.Small.Telegram != "ok" {
		t.Errorf("малый: %+v", got.Small)
	}
	if got.Full.Offsite != "ok" || got.Full.SizeBytes != 190_000_000 || got.Full.Telegram != "off" {
		t.Errorf("полный: %+v", got.Full)
	}
	if !got.Verify.OK || got.Verify.LastRunAt != "2026-10-04T03:30:00Z" || got.Verify.Routers != 3 {
		t.Errorf("проверка: %+v", got.Verify)
	}

	// Файл изменился, но кэш ещё жив: тот же ответ без чтения диска.
	writeBackupStatus(t, path, func(s *backup.Status) { s.Small.OK = false; s.Small.Error = "x" })
	clk.advance(10 * time.Second)
	if again := src.Summary(); !again.Small.OK {
		t.Fatal("кэш не работает: файл прочитан заново раньше срока")
	}
	clk.advance(backupStatusCacheTTL)
	if fresh := src.Summary(); fresh.Small.OK {
		t.Fatal("кэш не истёк: показано старое")
	}

	// Файл пропал после удачного чтения: через срок кэша -- «неизвестно».
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clk.advance(backupStatusCacheTTL + time.Second)
	if src.Summary().Known {
		t.Fatal("файл пропал, а состояние осталось известным")
	}
}

// В сводке нет путей и сырых текстов ошибок: только причина словами.
func TestBackupSummaryNeverCarriesRawErrorsOrPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), backup.StatusFileName)
	raws := map[string]string{
		"архив не записан: open /var/lib/wg-monitor/backups/x.partial: permission denied":                          "архив не записан",
		"архив не ушёл в Telegram: он 50,0 МБ, а Telegram принимает до 45,0 МБ":                                    "больше лимита Telegram",
		"архив не ушёл в Telegram: Post \"https://api.telegram.org/bot<redacted>/sendDocument\": dial tcp: lookup": "Telegram не принял",
		"архив не скопирован на внешний сервер: сервер недоступен":                                                 "внешний сервер",
		"архив не собран: small database: create table x: disk I/O error":                                          "не удалось собрать",
		"read passphrase: open /etc/wg-monitor/backup-passphrase.txt: no such file or directory":                   "парольная фраза",
		"что-то совсем новое /home/user/секрет":                                                                    "в журнале службы",
		backup.RunUnfinishedText: "прогон не завершён",
	}
	clk := &fakeClock{t: time.Date(2026, 10, 7, 5, 0, 0, 0, time.UTC)}
	for raw, wantWords := range raws {
		writeBackupStatus(t, path, func(s *backup.Status) {
			goodBackupStatus(s)
			s.Small.OK, s.Small.Error = false, raw
			s.Full.OK, s.Full.Error = false, raw
			s.Verify.OK, s.Verify.Error = false, raw
		})
		clk.advance(time.Hour)
		got := NewBackupStatusSource(path, clk.now).Summary()
		blob, _ := json.Marshal(got)
		for _, leak := range []string{"/var/", "/etc/", "/home/", ".partial", "https://", "bot<", "disk I/O", "permission denied", "секрет"} {
			if strings.Contains(string(blob), leak) {
				t.Errorf("в сводке %q (из %q): %s", leak, raw, blob)
			}
		}
		if !strings.Contains(got.Small.Reason, wantWords) {
			t.Errorf("причина малого %q для %q: нет %q", got.Small.Reason, raw, wantWords)
		}
		if got.Small.Reason == "" || got.Full.Reason == "" || got.Verify.Reason == "" {
			t.Errorf("пустая причина при провале: %+v", got)
		}
	}
}

func TestBackupSummaryMarksUnfinishedRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), backup.StatusFileName)
	writeBackupStatus(t, path, func(s *backup.Status) {
		goodBackupStatus(s)
		s.Full.OK, s.Full.Error = false, backup.RunUnfinishedText
	})
	got := NewBackupStatusSource(path, (&fakeClock{t: time.Now()}).now).Summary()
	if !got.Full.Unfinished || got.Small.Unfinished {
		t.Fatalf("unfinished: малый %v полный %v", got.Small.Unfinished, got.Full.Unfinished)
	}
}

func TestDashboardSummaryAndFleetCarryBackup(t *testing.T) {
	stubLatestVersion(t, "v0.31.0")
	d, _, _, _ := seedMiniappFleet(t)
	path := filepath.Join(t.TempDir(), backup.StatusFileName)
	writeBackupStatus(t, path, goodBackupStatus)
	src := NewBackupStatusSource(path, time.Now)
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999, DashboardToken: "secret", BackupStatus: src})

	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/summary", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var dash struct {
		Backup *backupSummary `json:"backup"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &dash) != nil || dash.Backup == nil || !dash.Backup.Known || dash.Backup.Full.Offsite != "ok" {
		t.Fatalf("summary: код %d, backup %+v, тело %s", rec.Code, dash.Backup, rec.Body.String())
	}

	fleet := fleetResponse(t, fleetRequest(t, h, 999))
	if fleet.Backup == nil || !fleet.Backup.Known || fleet.Backup.Small.Telegram != "ok" {
		t.Fatalf("парк: %+v", fleet.Backup)
	}

	// Не подключено (нет источника) -- поля нет, а не «неизвестно».
	h2 := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	if f2 := fleetResponse(t, fleetRequest(t, h2, 999)); f2.Backup != nil {
		t.Fatalf("без источника: %+v", f2.Backup)
	}
}

func TestBackupReasonWordsForManifestProblems(t *testing.T) {
	cases := map[string]string{
		"в манифесте архива нет счётчиков (routers) -- архив собран без них, сверить нечем": "в архиве нет счётчиков для сверки",
		"в архиве нет хранилища hidemyname.json, записанного в манифесте":                   "в архиве нет хранилища, записанного при бэкапе",
		"в архиве операторов 1, в манифесте архива 2":                                       "числа в архиве не сходятся с записанными при бэкапе",
	}
	for raw, want := range cases {
		if got := backupReasonWords(raw); got != want {
			t.Errorf("%q -> %q, ждали %q", raw, got, want)
		}
	}
}
