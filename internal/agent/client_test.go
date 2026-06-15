package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestClient_SendReport_AttachesBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-abc", "0.1.0", 2*time.Second)
	rep := wire.Report{Timestamp: time.Now(), AgentVersion: "0.1.0"}
	if _, err := c.SendReport(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("auth header: %q", gotAuth)
	}
}

func TestClient_SendReport_PostsJSON(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	rep := wire.Report{
		Timestamp:    time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
		AgentVersion: "0.1.0",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok", DurationMs: 1}},
	}
	if _, err := c.SendReport(context.Background(), rep); err != nil {
		t.Fatal(err)
	}
	var got wire.Report
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.AgentVersion != "0.1.0" || len(got.Checks) != 1 {
		t.Errorf("body roundtrip: %+v", got)
	}
}

func TestClient_SendReport_ErrorsOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	_, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("err should mention 502: %v", err)
	}
}

func TestClient_AuthRejection_ReturnsTypedError(t *testing.T) {
	// 401/403 must surface as ErrUnauthorized so the cmdloop / reporter can
	// distinguish a rotated token from a transient 5xx and back off.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("invalid token"))
		}))
		c := NewClient(srv.URL, "stale", "0.1.0", 2*time.Second)
		_, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
		if err == nil {
			t.Fatalf("status=%d: expected error", status)
		}
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status=%d: err must wrap ErrUnauthorized, got %v", status, err)
		}
		// Same expectation for PollCommand and PostResult.
		if _, perr := c.PollCommand(context.Background(), 1); !errors.Is(perr, ErrUnauthorized) {
			t.Errorf("status=%d: PollCommand must surface ErrUnauthorized, got %v", status, perr)
		}
		if rerr := c.PostResult(context.Background(), wire.CommandResult{ID: "x", Status: "ok"}); !errors.Is(rerr, ErrUnauthorized) {
			t.Errorf("status=%d: PostResult must surface ErrUnauthorized, got %v", status, rerr)
		}
		srv.Close()
	}
}

func TestClient_SendReport_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.SendReport(ctx, wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on cancellation")
	}
}

func TestClient_PollCommand_204ReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cmd" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method: %q", r.Method)
		}
		if r.URL.Query().Get("wait") != "10" {
			t.Errorf("wait param: %q", r.URL.Query().Get("wait"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", "0.1.0", 2*time.Second)
	cmd, err := c.PollCommand(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != nil {
		t.Errorf("expected nil on 204, got %+v", cmd)
	}
}

func TestClient_PollCommand_200ReturnsCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc","action":"diag_now","issued_at":"2026-04-29T12:00:00Z"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", "0.1.0", 2*time.Second)
	cmd, err := c.PollCommand(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.ID != "abc" || cmd.Action != "diag_now" {
		t.Errorf("got %+v", cmd)
	}
}

func TestClient_PollCommand_RejectsOversizeValidPrefix(t *testing.T) {
	const limit = 64 * 1024
	prefix := `{"id":"abc","action":"diag_now","pad":"`
	suffix := `"}`
	padLen := limit - len(prefix) - len(suffix)
	if padLen <= 0 {
		t.Fatalf("bad test fixture pad length: %d", padLen)
	}
	body := prefix + strings.Repeat("A", padLen) + suffix + "X"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", "0.1.0", 2*time.Second)
	_, err := c.PollCommand(context.Background(), 5)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want oversize response error, got %v", err)
	}
}

func TestClient_PollCommand_5xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", "0.1.0", 2*time.Second)
	_, err := c.PollCommand(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestClient_PostResult_OK(t *testing.T) {
	var gotBody []byte
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok-z", "0.1.0", 2*time.Second)
	res := wire.CommandResult{ID: "abc", Status: "ok", DurationMs: 42}
	if err := c.PostResult(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/cmd/result" {
		t.Errorf("path: %q", gotPath)
	}
	if gotAuth != "Bearer tok-z" {
		t.Errorf("auth: %q", gotAuth)
	}
	var back wire.CommandResult
	if err := json.Unmarshal(gotBody, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.ID != "abc" || back.Status != "ok" {
		t.Errorf("back: %+v", back)
	}
}

func TestClient_PostResult_5xxErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok", "0.1.0", 2*time.Second)
	err := c.PostResult(context.Background(), wire.CommandResult{ID: "x", Status: "ok"})
	if err == nil {
		t.Fatal("expected error on 502")
	}
}

func TestClient_SendReport_RecordsMetrics(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	for i := 0; i < 3; i++ {
		if _, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("hits: %d want 3", hits)
	}
}

func TestClient_SendReport_ReturnsCanonicalURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"canonical_url":"https://new.example.com"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	canonicalURL, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if canonicalURL != "https://new.example.com" {
		t.Errorf("canonical URL: got %q, want https://new.example.com", canonicalURL)
	}
}

func TestClient_SendReport_EmptyBodyReturnsEmptyURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "t", "0.1.0", 2*time.Second)
	canonicalURL, err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if canonicalURL != "" {
		t.Errorf("expected empty canonical URL, got %q", canonicalURL)
	}
}
