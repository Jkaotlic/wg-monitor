package agent

import (
	"context"
	"encoding/json"
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
	if err := c.SendReport(context.Background(), rep); err != nil {
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
	if err := c.SendReport(context.Background(), rep); err != nil {
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
	err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("err should mention 502: %v", err)
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
	err := c.SendReport(ctx, wire.Report{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on cancellation")
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
		if err := c.SendReport(context.Background(), wire.Report{Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if atomic.LoadInt32(&hits) != 3 {
		t.Errorf("hits: %d want 3", hits)
	}
}
