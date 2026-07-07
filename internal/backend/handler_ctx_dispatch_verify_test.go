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
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// ctxProbeDispatcher is a Dispatcher fake used only to detect whether the
// context passed to Handle gets cancelled out from under an in-flight call
// (C6 verify). Handle blocks until EITHER ctx is cancelled OR the test
// releases it via proceed — whichever happens first is recorded, modeling
// an in-flight TG send that would itself observe ctx cancellation.
type ctxProbeDispatcher struct {
	entered chan struct{}
	proceed chan struct{}
	result  chan string // "completed" or "aborted:<err>"
}

func (f *ctxProbeDispatcher) Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, check wire.Check) error {
	close(f.entered)
	select {
	case <-ctx.Done():
		err := ctx.Err()
		f.result <- "aborted:" + err.Error()
		return err
	case <-f.proceed:
		f.result <- "completed"
		return nil
	}
}

// TestReportHandler_ClientDisconnectDuringDispatch_Verify is the C6 VERIFY
// step (disputed audit finding), now also the regression test for the fix:
// cancelling the AGENT's report POST mid-handler (simulating the agent's
// HTTP client disconnecting — NOT a server shutdown) must NOT abort the
// in-flight HARD/Recovery Dispatcher.Handle call at handler.go's primary
// dispatch site. Before the fix this reproduced (verdict "aborted:context
// canceled") because that call site passed r.Context() straight through;
// the 2026-06-18 audit had only checked server shutdown as a cancellation
// source, missing that net/http also cancels r.Context() when the client's
// connection closes, independent of srv.Shutdown. The fix switches that
// call to a server-owned context (relayParent(d)) with its own bounded
// timeout, so the send now completes despite the request ctx cancelling.
func TestReportHandler_ClientDisconnectDuringDispatch_Verify(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01cd01"
	uid, err := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	// Pre-seed state one fail below the HARD threshold (Fail:2) so this
	// report's single "fail" check crosses it immediately (state.Hard),
	// reaching the primary dispatch call without needing multiple round trips.
	if err := d.State().Save(uid, "dns", db.IncidentState{CurrentStatus: "fail", ConsecutiveFails: 1}); err != nil {
		t.Fatal(err)
	}

	probe := &ctxProbeDispatcher{
		entered: make(chan struct{}),
		proceed: make(chan struct{}),
		result:  make(chan string, 1),
	}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  probe,
		CommandSink: &fakeCmdSink{},
		Thresholds:  state.Thresholds{Fail: 2, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks:       []wire.Check{{Name: "dns", Status: "fail"}},
	})

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, srv.URL+"/v1/report", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	clientDone := make(chan error, 1)
	go func() {
		resp, doErr := http.DefaultClient.Do(req)
		if doErr == nil {
			resp.Body.Close()
		}
		clientDone <- doErr
	}()

	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Dispatcher.Handle was never entered — test setup didn't reach HARD dispatch")
	}

	// Simulate the agent's report POST disconnecting mid-handler: cancel the
	// CLIENT's request context. This aborts the in-flight HTTP request and
	// closes the underlying connection from the client side — a distinct
	// cancellation source from server shutdown (srv.Shutdown, BE-02).
	cancelReq()
	<-clientDone

	var verdict string
	select {
	case verdict = <-probe.result:
	case <-time.After(500 * time.Millisecond):
		// Handle is still blocked: ctx.Done() did not fire from the client
		// disconnect within our window (a context cancellation, if it were
		// going to propagate at all, does so near-instantly). Release it so
		// the test doesn't hang; this itself is evidence of "not aborted".
		close(probe.proceed)
		verdict = <-probe.result
	}

	t.Logf("C6 verify verdict: %s", verdict)
	if strings.HasPrefix(verdict, "aborted:") {
		t.Errorf("REPRODUCED: client disconnect aborted in-flight Dispatcher.Handle via r.Context() (%s) — handler.go's primary HARD/Recovery dispatch call must switch to a server-owned context (relayParent(d))", verdict)
	}
}
