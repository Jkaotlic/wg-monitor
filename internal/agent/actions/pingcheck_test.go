package actions

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

func TestPingCheckStatus_PassesThroughJSON(t *testing.T) {
	const pingCheckResp = `{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"enabled":true,"tunnelRunning":true}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pingcheck/status":
			if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
				t.Errorf("missing X-Requested-With header")
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(pingCheckResp))
		case "/api/tunnels/all":
			// Non-fatal: return 500 to exercise the graceful-degrade path.
			w.WriteHeader(500)
		default:
			t.Errorf("unexpected path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
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

func TestPingCheckToggle_PrimaryPathOK(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		// Path verified against Task 1 step 1 finding. Update if different.
		if r.URL.Path != "/api/pingcheck/toggle" {
			t.Errorf("path: %q", r.URL.Path)
		}
		posted = true
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("ndmc should NOT be called when POST succeeds; got %s %v", name, args)
		return nil, nil
	})
	if err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !posted {
		t.Error("primary-path POST was not attempted")
	}
}

func TestPingCheckToggle_PrimaryFailsFallbackOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	var ndmcCalled bool
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ndmcCalled = true
		if name != "ndmc" {
			t.Errorf("expected ndmc, got %s", name)
		}
		// Exact ndmc syntax assumed per Task 1 step 2 — `interface <name> ping-check`
		// to enable, prefixed with `no` to disable. User will verify on testkeen.
		want := `no interface Wireguard0 ping-check`
		if len(args) < 2 || args[1] != want {
			t.Errorf("ndmc args mismatch: got %v, want -c %q", args, want)
		}
		return []byte("ok"), nil
	})
	if err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", false); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ndmcCalled {
		t.Error("ndmc fallback was not invoked")
	}
}

func TestPingCheckToggle_PrimarySuccessFalseFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"message":"denied"}`))
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	var ndmcCalled bool
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ndmcCalled = true
		return []byte("ok"), nil
	})

	err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", true)

	if err != nil {
		t.Fatalf("expected ndmc fallback to recover, got %v", err)
	}
	if !ndmcCalled {
		t.Fatal("expected ndmc fallback after success=false envelope")
	}
}

func TestPingCheckToggle_BothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("ndmc: interface unknown"), errors.New("exit 1")
	})
	err := PingCheckToggle(context.Background(), c, exec, "awg10", "Wireguard0", true)
	if err == nil {
		t.Fatal("expected aggregated err")
	}
	msg := err.Error()
	if !strings.Contains(msg, "POST") || !strings.Contains(msg, "ndmc") {
		t.Errorf("err must aggregate both paths: %v", err)
	}
}

func TestPingCheckStatus_EnrichesNDMSName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pingcheck/status":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","tunnelName":"amst","status":"alive","enabled":true,"lastLatency":42,"failCount":0,"successCount":100,"failThreshold":3,"restartCount":0,"tunnelRunning":true}]}}`))
		case "/api/tunnels/all":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"external":[],"system":[],"tunnels":[{"id":"awg10","ndmsName":"Wireguard0","name":"amst"}]}}`))
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := PingCheckStatusJSON(context.Background(), c)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(out, `"ndmsName":"Wireguard0"`) {
		t.Errorf("expected enriched NDMSName in output, got: %s", out)
	}
}

func TestPingCheckStatus_TunnelsAllFailNonFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pingcheck/status":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg10","status":"alive","enabled":true}]}}`))
		case "/api/tunnels/all":
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	out, err := PingCheckStatusJSON(context.Background(), c)
	if err != nil {
		t.Fatalf("expected non-fatal on tunnels/all failure: %v", err)
	}
	if !strings.Contains(out, `"tunnelId":"awg10"`) {
		t.Errorf("expected status passthrough even with empty NDMSName, got: %s", out)
	}
}
