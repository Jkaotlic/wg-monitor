package backend

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type recordingUnstick struct {
	mu  sync.Mutex
	evs []wire.UnstickEvent
}

func (r *recordingUnstick) SendUnstick(_ context.Context, _ int64, _ string, evs []wire.UnstickEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, evs...)
	return nil
}

func (r *recordingUnstick) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		out = append(out, e.ID)
	}
	return out
}

// waitIDs ждёт (не дольше 2 с), пока получателю не придёт want сообщений.
func (r *recordingUnstick) waitIDs(t *testing.T, want int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.ids(); len(got) >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// короткая пауза: лишнее сообщение не должно прийти следом
	time.Sleep(100 * time.Millisecond)
	return r.ids()
}

// TestReport_UnstickEventNotifiedOnce: один и тот же журнал в двух отчётах
// подряд -- владельцу одно сообщение; новое событие -- ещё одно; gave_up и
// старше часа -- ни одного.
func TestReport_UnstickEventNotifiedOnce(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tok := "4848484848484848484848484848484848484848484848484848484848484848"
	uid, err := d.Users().Insert("v059", tok, "198.51.100.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingUnstick{}
	srv := httptest.NewServer(NewMux(Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              d,
		Dispatcher:      &fakeDisp{db: d},
		Thresholds:      state.Thresholds{Fail: 3, Recovery: 2},
		UnstickNotifier: rec,
	}))
	t.Cleanup(srv.Close)

	post := func(evs ...wire.UnstickEvent) {
		postFactsReport(t, srv, tok, wire.Report{
			Timestamp:    time.Now().UTC(),
			AgentVersion: "v0.59.0",
			Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
			Facts:        &wire.ReportFacts{Unstick: &wire.UnstickFacts{Events: evs}},
		})
	}
	now := time.Now().UTC().Truncate(time.Second)
	ev1 := wire.UnstickEvent{ID: "1-nwg0", TunnelID: "nwg0", From: "broken", Steps: []string{"restart"}, Result: wire.UnstickFixed, To: "running", At: now}
	post(ev1)
	post(ev1)
	if got := rec.waitIDs(t, 1); len(got) != 1 || got[0] != "1-nwg0" {
		t.Fatalf("notified: %v", got)
	}
	ev2 := ev1
	ev2.ID = "2-nwg0"
	gave := wire.UnstickEvent{ID: "3-nwg1", TunnelID: "nwg1", From: "broken", Result: wire.UnstickGaveUp, At: now}
	old := wire.UnstickEvent{ID: "4-nwg2", TunnelID: "nwg2", From: "broken", Result: wire.UnstickFixed, At: now.Add(-2 * time.Hour)}
	post(ev1, ev2, gave, old)
	if got := rec.waitIDs(t, 2); len(got) != 2 || got[1] != "2-nwg0" {
		t.Fatalf("notified: %v", got)
	}
	if n, _ := d.UnstickEvents().Count(uid); n != 4 {
		t.Errorf("stored events: %d", n)
	}
}
