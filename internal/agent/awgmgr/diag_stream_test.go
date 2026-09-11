package awgmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDiagFresh_AsksForNoRestart pins the exact request DiagFresh must send:
// GET /api/diagnostics/stream?restart=false with an SSE Accept header. This
// is the whole point of the task — the old diag_now path could trigger a
// tunnel-restarting run; DiagFresh must never ask for one.
func TestDiagFresh_AsksForNoRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/stream" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("restart"); got != "false" {
			t.Errorf("restart query param: got %q, want %q", got, "false")
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("Accept header: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "event: phase\ndata: {\"type\":\"phase\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		fmt.Fprint(w, "event: test\ndata: {\"type\":\"test\"}\n\n")
		if fl != nil {
			fl.Flush()
		}
		fmt.Fprint(w, "event: done\ndata: {\"type\":\"done\",\"summary\":{\"total\":34,\"passed\":25,\"failed\":0,\"skipped\":9,\"hasReport\":true}}\n\n")
		if fl != nil {
			fl.Flush()
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if err := c.DiagFresh(context.Background()); err != nil {
		t.Fatalf("DiagFresh: %v", err)
	}
}

// TestDiagFresh_OutlivesClientTimeout proves DiagFresh issues the stream
// request on a client whose Timeout is not the ambient c.HTTP.Timeout: a
// stream held open for 400ms must survive a Client{HTTP.Timeout: 100ms}.
// http.Client.Timeout bounds the whole request including reading the body,
// so if DiagFresh reused c.HTTP as-is this test would fail with a client
// timeout error instead of returning nil.
func TestDiagFresh_OutlivesClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl := w.(http.Flusher)
		fmt.Fprint(w, "event: phase\ndata: {\"type\":\"phase\"}\n\n")
		fl.Flush()
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "event: done\ndata: {\"type\":\"done\"}\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 100 * time.Millisecond}}
	if err := c.DiagFresh(context.Background()); err != nil {
		t.Fatalf("DiagFresh should outlive c.HTTP.Timeout, got: %v", err)
	}
}

// TestDiagFresh_OldPanel404 covers awg-manager builds older than 2.12, which
// don't serve /api/diagnostics/stream at all.
func TestDiagFresh_OldPanel404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.DiagFresh(context.Background())
	if !errors.Is(err, ErrDiagStreamUnsupported) {
		t.Fatalf("expected ErrDiagStreamUnsupported, got: %v", err)
	}
}

// TestDiagFresh_ErrorEvent covers awg-manager reporting a run failure over
// the stream itself (e.g. a panic mid-check).
func TestDiagFresh_ErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"message\":\"panic: x\"}\n\n")
	}))
	defer srv.Close()

	c := New(srv.URL)
	err := c.DiagFresh(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DIAG_STREAM_ERROR") || !strings.Contains(err.Error(), "panic: x") {
		t.Fatalf("expected DIAG_STREAM_ERROR with 'panic: x', got: %v", err)
	}
}

// TestDiagFresh_CutStreamWaitsForStatus: the stream connection can drop
// before "done" arrives without the underlying run stopping — awg-manager
// runs it in context.Background(). DiagFresh must notice the drop and poll
// /api/diagnostics/status until the run is no longer "running".
func TestDiagFresh_CutStreamWaitsForStatus(t *testing.T) {
	var statusHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/diagnostics/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			fmt.Fprint(w, "event: phase\ndata: {\"type\":\"phase\"}\n\n")
			fl.Flush()
			// Connection drops here — no "done"/"error" ever arrives.
		case "/api/diagnostics/status":
			statusHits++
			w.WriteHeader(200)
			if statusHits < 3 {
				_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
			} else {
				_, _ = w.Write([]byte(`{"success":true,"data":{"status":"done"}}`))
			}
		default:
			t.Errorf("unexpected path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	diagStatusPollInterval = time.Millisecond
	t.Cleanup(func() { diagStatusPollInterval = defaultDiagStatusPollInterval })

	if err := c.DiagFresh(context.Background()); err != nil {
		t.Fatalf("DiagFresh: %v", err)
	}
	if statusHits < 2 {
		t.Errorf("expected status polled at least twice, got %d", statusHits)
	}
}

// TestDiagFresh_UsesSessionCookie proves the stream request carries the same
// session cookie as every other client call once credentials are set.
func TestDiagFresh_UsesSessionCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		case "/api/diagnostics/stream":
			ck, err := r.Cookie("awg_session")
			if err != nil || ck.Value != "session-1" {
				t.Fatalf("missing session cookie on stream request: %v / %#v", err, ck)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprint(w, "event: done\ndata: {\"type\":\"done\"}\n\n")
		default:
			t.Fatalf("unexpected path: %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetCredentials("admin", "secret")
	if err := c.DiagFresh(context.Background()); err != nil {
		t.Fatalf("DiagFresh: %v", err)
	}
}
