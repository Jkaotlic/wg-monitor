package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeDisp struct {
	mu    sync.Mutex
	calls []state.Kind
}

func (f *fakeDisp) Handle(_ context.Context, _ int64, _, _ string, tr state.Transition, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tr.Kind)
	return nil
}

func TestReportPersistsEventsAndDispatches(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	disp := &fakeDisp{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks: []wire.Check{
			{Name: "agent_heartbeat", Status: "ok"},
			{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "stale"}},
		},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher invoked %d times (heartbeat must NOT trigger)", len(disp.calls))
	}
	if disp.calls[0] != state.Soft {
		t.Fatalf("kind: %v", disp.calls[0])
	}
	disp.mu.Unlock()

	latest, _ := d.Events().LatestPerUser(uid)
	if latest.IsZero() {
		t.Fatal("event not persisted")
	}
}

func TestReportRejectsTooLarge(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	huge := bytes.Repeat([]byte("A"), 80*1024)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(huge))
	req.Header.Set("Authorization", "Bearer x")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
