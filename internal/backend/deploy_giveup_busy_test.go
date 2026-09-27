package backend

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DEP-01: «сервер обновлений был занят, повторим» -- не причина сдачи.
// Попытки кончились на потерянных командах, а последней записанной
// причиной осталась занятость прокси: финальный текст обещал бы повтор,
// которого не будет.
func TestGiveUpAfterBusyDoesNotPromiseRetry(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, _ := d.Users().Insert("router-dep", strings.Repeat("de", 32), "198.51.100.30", "awg0")
	if err := d.Users().MarkPendingDeploy(uid, "v0.46.0", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < pendingDeployMaxAttempts; i++ {
		if _, _, err := d.Users().IncrementPendingAttempts(uid, "v0.46.0"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := d.Users().RecordPendingDeployError(uid, "v0.46.0", wire.SelfUpdateBusyMarker+": backend busy"); err != nil {
		t.Fatal(err)
	}
	notifier := &fakeDeployNotifier{}
	deps := Deps{DB: d, DeployNotifier: notifier, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if !giveUpIfExhausted(deps, uid, "router-dep") {
		t.Fatal("попытки исчерпаны -- сдача")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(notifier.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	calls := notifier.snapshot()
	if len(calls) != 1 {
		t.Fatalf("уведомлений %d", len(calls))
	}
	if strings.Contains(calls[0].output, "занят") || strings.Contains(calls[0].output, "повторим") {
		t.Fatalf("финальный текст обещает повтор: %q", calls[0].output)
	}
}
