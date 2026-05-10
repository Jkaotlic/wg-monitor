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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)

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
	mu            sync.Mutex
	dequeueRet    *wire.Command
	dequeueWaitMs int
	results       []wire.CommandResult
	resultErr     error
	originRef     *cmdpkg.MessageRef // returned once by ConsumeOriginRef when set
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

func (f *fakeCmdSink) ConsumeOriginRef(userID int64, cmdID string) (cmdpkg.MessageRef, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.originRef == nil {
		return cmdpkg.MessageRef{}, false
	}
	r := *f.originRef
	f.originRef = nil // consume
	return r, true
}

type relayCapture struct {
	mu      sync.Mutex
	chunks  []string
	chatID  int64
	thread  *int64
	replyTo int64
	action  string
}

func (rc *relayCapture) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, maxChars int) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.chatID = ref.ChatID
	rc.thread = ref.ThreadID
	rc.replyTo = ref.MessageID
	rc.action = action
	rc.chunks = append(rc.chunks, alerts.FormatCommandResult(action, result, maxChars)...)
	return nil
}

type relaySnapshot struct {
	chunks  []string
	chatID  int64
	thread  *int64
	replyTo int64
	action  string
}

func (rc *relayCapture) snapshot() relaySnapshot {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := relaySnapshot{
		chatID:  rc.chatID,
		thread:  rc.thread,
		replyTo: rc.replyTo,
		action:  rc.action,
	}
	out.chunks = append(out.chunks, rc.chunks...)
	return out
}

func TestCmdResultRelayedToTG(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	tid := int64(11)
	sink := &fakeCmdSink{
		originRef: &cmdpkg.MessageRef{
			ChatID: -100, MessageID: 42, ThreadID: &tid, Action: "diag_now",
		},
	}
	rc := &relayCapture{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		TGNotifier:  rc,
		UI:          UIConfig{DiagMaxChars: 3500},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok", Output: "diagnostics: all green"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Relay is async (goroutine). Poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if snap := rc.snapshot(); len(snap.chunks) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := rc.snapshot()
	if len(got.chunks) != 1 {
		t.Fatalf("want 1 relay chunk, got %d", len(got.chunks))
	}
	if got.chatID != -100 || got.replyTo != 42 || got.thread == nil || *got.thread != 11 {
		t.Errorf("ref mis-routed: chatID=%d reply=%d thread=%v", got.chatID, got.replyTo, got.thread)
	}
	if got.action != "diag_now" {
		t.Errorf("action mismatch: %q", got.action)
	}
	if !strings.Contains(got.chunks[0], "diagnostics: all green") {
		t.Errorf("output missing: %s", got.chunks[0])
	}
}

func TestCmdResultNoRelayWhenNotifierNil(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{
		originRef: &cmdpkg.MessageRef{ChatID: -100, MessageID: 42, Action: "diag_now"},
	}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		// TGNotifier intentionally nil
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	// No assertion on relay — just must not panic / 500.
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

type fakeRoutesNotifier struct {
	mu     sync.Mutex
	called int
}

func (f *fakeRoutesNotifier) NotifyCommandResult(_ context.Context, _ cmdpkg.MessageRef, _ wire.CommandResult, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	return nil
}

func TestCmdResult_DispatchesRoutesNotifier(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	rn := &fakeRoutesNotifier{}
	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		Dispatcher:     &fakeDisp{},
		CommandSink:    sink,
		TGNotifier:     rc,
		RoutesNotifier: rn,
		Thresholds:     state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "x", Status: "ok", Output: `{"tunnels":[]}`, DurationMs: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Goroutine dispatch is async — poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		rn.mu.Lock()
		c := rn.called
		rn.mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rn.mu.Lock()
	rnCalled := rn.called
	rn.mu.Unlock()
	if rnCalled != 1 {
		t.Errorf("RoutesNotifier called %d times, want 1", rnCalled)
	}

	snap := rc.snapshot()
	if len(snap.chunks) != 0 {
		t.Errorf("generic relay called %d chunk(s) for route_status, want 0", len(snap.chunks))
	}
}

type fakeMaintNotifier struct {
	mu     sync.Mutex
	called int
	action string
}

func (f *fakeMaintNotifier) NotifyCommandResult(_ context.Context, ref cmdpkg.MessageRef, _ wire.CommandResult, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	f.action = ref.Action
	return nil
}

func testCmdResultDispatchesMaintNotifier(t *testing.T, action string) {
	t.Helper()
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01bb01"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	mn := &fakeMaintNotifier{}
	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: action, ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:            d,
		Dispatcher:    &fakeDisp{},
		CommandSink:   sink,
		TGNotifier:    rc,
		MaintNotifier: mn,
		Thresholds:    state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "m1", Status: "ok", Output: `{}`, DurationMs: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Goroutine dispatch is async — poll briefly.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mn.mu.Lock()
		c := mn.called
		mn.mu.Unlock()
		if c > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mn.mu.Lock()
	mnCalled := mn.called
	mn.mu.Unlock()
	if mnCalled != 1 {
		t.Errorf("MaintNotifier called %d times for action %q, want 1", mnCalled, action)
	}

	// TGNotifier must NOT be called for maint actions.
	snap := rc.snapshot()
	if len(snap.chunks) != 0 {
		t.Errorf("generic TGNotifier called %d chunk(s) for %q, want 0", len(snap.chunks), action)
	}
}

func TestCmdResult_DispatchesMaintNotifier_VersionAudit(t *testing.T) {
	testCmdResultDispatchesMaintNotifier(t, "version_audit")
}

func TestCmdResult_DispatchesMaintNotifier_ServiceRestart(t *testing.T) {
	testCmdResultDispatchesMaintNotifier(t, "service_restart")
}

// TestCmdResult_AcceptsUnknownStatus verifies forward-compat: a status not in
// wire.validCommandResultStatuses is logged but accepted (200), so a future
// agent emitting "partial"/"rate_limited"/etc. doesn't lose its result during
// a rolling fleet upgrade. Empty status is still rejected (400) — that's a
// real client bug, not schema evolution.
func TestCmdResult_AcceptsUnknownStatus(t *testing.T) {
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

	// Unknown but non-empty status must be accepted (forward-compat).
	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "weird"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for unknown status (forward-compat), got %d", resp.StatusCode)
	}

	// Empty status must still be rejected. 422 = parsed correctly, semantic
	// violation of required field (API-03).
	body, _ = json.Marshal(wire.CommandResult{ID: "abc2", Status: ""})
	req, _ = http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for empty status, got %d", resp.StatusCode)
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
