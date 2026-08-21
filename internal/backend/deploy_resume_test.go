package backend

import (
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type recordingEnqueuer struct {
	mu   sync.Mutex
	sent []struct {
		userID int64
		cmd    wire.Command
	}
}

func (r *recordingEnqueuer) Enqueue(userID int64, cmd wire.Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, struct {
		userID int64
		cmd    wire.Command
	}{userID, cmd})
	return nil
}

func resumeTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// Очередь команд живёт в памяти, отметка «обновление назначено» -- в базе.
// Рестарт бэкенда стирал первое и оставлял второе: роутер месяцами числился
// «ждёт v0.18.5», хотя команды ему уже никто не пошлёт. Дашборд при этом
// показывал ожидание, а операция была потеряна.
func TestResumePendingDeploys_ReenqueuesAfterRestart(t *testing.T) {
	d := resumeTestDB(t)
	uid, err := d.Users().Insert("vasya", strings.Repeat("a", 64), "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(uid, "v0.18.5", "2026-08-21T08:13:42Z"); err != nil {
		t.Fatal(err)
	}

	sink := &recordingEnqueuer{}
	n := ResumePendingDeploys(d, sink, "https://wgmonitor.example", "203.0.113.5", nil)

	if n != 1 {
		t.Fatalf("возобновлено %d, ждали 1", n)
	}
	if len(sink.sent) != 1 {
		t.Fatalf("в очередь ушло %d команд", len(sink.sent))
	}
	got := sink.sent[0]
	if got.userID != uid || got.cmd.Action != "self_update" {
		t.Fatalf("не та команда: user=%d action=%s", got.userID, got.cmd.Action)
	}
	if got.cmd.Args["version"] != "v0.18.5" {
		t.Errorf("версия: %v", got.cmd.Args["version"])
	}
	if got.cmd.Args["repo_base"] != "https://wgmonitor.example/v1/releases/download" {
		t.Errorf("repo_base: %v", got.cmd.Args["repo_base"])
	}
	if got.cmd.Args["repo_resolve_ip"] != "203.0.113.5" {
		t.Errorf("repo_resolve_ip: %v", got.cmd.Args["repo_resolve_ip"])
	}
	if got.cmd.ID == "" {
		t.Error("у команды нет идентификатора")
	}
}

func TestResumePendingDeploys_SkipsRoutersWithoutPending(t *testing.T) {
	d := resumeTestDB(t)
	if _, err := d.Users().Insert("vasya", strings.Repeat("b", 64), "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &recordingEnqueuer{}
	if n := ResumePendingDeploys(d, sink, "https://wgmonitor.example", "", nil); n != 0 {
		t.Fatalf("возобновлено %d, ждали 0", n)
	}
	if len(sink.sent) != 0 {
		t.Fatalf("лишние команды: %d", len(sink.sent))
	}
}

// Без публичного адреса собрать ссылку на бинарь нечем: агент по такой
// команде всё равно ничего не скачает. Молча слать её -- значит снова
// оставить роутер в ожидании, которое ничем не кончится.
func TestResumePendingDeploys_NeedsPublicBaseURL(t *testing.T) {
	d := resumeTestDB(t)
	uid, err := d.Users().Insert("vasya", strings.Repeat("c", 64), "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().MarkPendingDeploy(uid, "v0.18.5", "2026-08-21T08:13:42Z"); err != nil {
		t.Fatal(err)
	}
	sink := &recordingEnqueuer{}
	if n := ResumePendingDeploys(d, sink, "  ", "", nil); n != 0 {
		t.Fatalf("возобновлено %d, ждали 0", n)
	}
}

func TestResumePendingDeploys_NilInputsAreSafe(t *testing.T) {
	if n := ResumePendingDeploys(nil, nil, "https://x", "", nil); n != 0 {
		t.Fatalf("возобновлено %d, ждали 0", n)
	}
}
