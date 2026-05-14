package actions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

func TestPingCheckStatus_PassesThroughJSON(t *testing.T) {
	const want = `{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"enabled":true,"tunnelRunning":true}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pingcheck/status" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(want))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := PingCheckStatusJSON(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "alive") {
		t.Errorf("expected status passthrough, got: %s", out)
	}
}

func TestPingCheckStatus_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("upstream gone"))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	_, err := PingCheckStatusJSON(context.Background(), c)
	if err == nil {
		t.Fatal("expected err on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err must mention status: %v", err)
	}
}
