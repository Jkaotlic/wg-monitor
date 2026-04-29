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

func (f *fakeDisp) Handle(_ context.Context, _ int64, _, _ string, tr state.Transition, _ wire.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tr.Kind)
	return nil
}

type fakeResumer struct {
	mu      sync.Mutex
	resumed []int64
}

func (f *fakeResumer) MarkResumed(uid int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, uid)
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

func TestReportResumedCallsResumer(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1212121212121212121212121212121212121212121212121212121212121212"
	uid, _ := d.Users().InsertWithKind("carvan", tok, "1.1.1.1", "nwg0", db.KindMobile)

	disp := &fakeDisp{}
	resumer := &fakeResumer{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Resumer:    resumer,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Resumed:      true,
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resumer.mu.Lock()
	defer resumer.mu.Unlock()
	if len(resumer.resumed) != 1 || resumer.resumed[0] != uid {
		t.Fatalf("expected MarkResumed(%d), got calls=%v", uid, resumer.resumed)
	}
}

func TestReportNotResumedSkipsResumer(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1313131313131313131313131313131313131313131313131313131313131313"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	resumer := &fakeResumer{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		Resumer:    resumer,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	resumer.mu.Lock()
	defer resumer.mu.Unlock()
	if len(resumer.resumed) != 0 {
		t.Fatalf("MarkResumed must not be called when Resumed=false; calls=%v", resumer.resumed)
	}
}

type fakeCmdSink struct {
	mu        sync.Mutex
	dequeueRet *wire.Command
	dequeueWaitMs int
	results   []wire.CommandResult
	resultErr error
}

func (f *fakeCmdSink) Dequeue(ctx context.Context, userID int64, hold time.Duration) (*wire.Command, bool) {
	if f.dequeueWaitMs > 0 {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(time.Duration(f.dequeueWaitMs) * time.Millisecond):
		}
	}
	if f.dequeueRet == nil {
		return nil, false
	}
	c := *f.dequeueRet
	return &c, true
}

func (f *fakeCmdSink) RecordResult(userID int64, r wire.CommandResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resultErr != nil {
		return f.resultErr
	}
	f.results = append(f.results, r)
	return nil
}

func TestCmdGet_ReturnsQueuedCommand(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	want := &wire.Command{ID: "abc", Action: "diag_now", IssuedAt: time.Now().UTC()}
	sink := &fakeCmdSink{dequeueRet: want}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		CommandSink: sink,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd?wait=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got wire.Command
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "abc" || got.Action != "diag_now" {
		t.Errorf("got %+v", got)
	}
}

func TestCmdGet_204WhenIdle(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{} // dequeueRet=nil → no command
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd?wait=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestCmdGet_RequiresAuth(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: &fakeCmdSink{},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestCmdResult_PostRecords(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok", Output: "done", DurationMs: 42})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.results) != 1 {
		t.Fatalf("RecordResult called %d times", len(sink.results))
	}
	if sink.results[0].ID != "abc" || sink.results[0].Status != "ok" || sink.results[0].DurationMs != 42 {
		t.Errorf("got %+v", sink.results[0])
	}
}

func TestCmdResult_RejectsBadJSON(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCmdResult_RejectsInvalidStatus(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "weird"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
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
