package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// awgmgrFake hosts httptest endpoints awg-manager would serve.
func awgmgrFake(t *testing.T, h http.Handler) *awgmgr.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return awgmgr.New(srv.URL)
}

func mockNow() func() time.Time {
	t := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(50 * time.Millisecond)
		return t
	}
}

func TestRunner_RestartTunnel_OK(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/control/restart-all" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "restart_tunnel"})
	if res.Status != "ok" {
		t.Errorf("status: %q output=%q", res.Status, res.Output)
	}
	if res.ID != "c1" {
		t.Errorf("id: %q", res.ID)
	}
	if res.DurationMs <= 0 {
		t.Errorf("duration_ms not populated: %d", res.DurationMs)
	}
}

func TestRunner_RestartTunnel_Err(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c1", Action: "restart_tunnel"})
	if res.Status != "err" {
		t.Errorf("expected err status, got %q", res.Status)
	}
	if !strings.Contains(res.Output, "500") {
		t.Errorf("expected output to contain 500, got %q", res.Output)
	}
}

func TestRunner_PingcheckNow_OK(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pingcheck/check-now" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c2", Action: "pingcheck_now"})
	if res.Status != "ok" {
		t.Errorf("status: %q", res.Status)
	}
}

func TestRunner_DiagNow_PassesThroughBody(t *testing.T) {
	const wantPayload = `{"success":true,"data":{"summary":"4/4 green"}}`
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/result" {
			t.Errorf("path: %q", r.URL.Path)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(wantPayload))
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c3", Action: "diag_now"})
	if res.Status != "ok" {
		t.Errorf("status: %q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "4/4 green") {
		t.Errorf("expected payload in output, got %q", res.Output)
	}
}

func TestRunner_ForceRecheck_CallsCallback(t *testing.T) {
	called := 0
	r := Runner{
		Now:          mockNow(),
		ForceRecheck: func(ctx context.Context) { called++ },
	}
	res := r.Execute(context.Background(), wire.Command{ID: "c4", Action: "force_recheck"})
	if res.Status != "ok" {
		t.Errorf("status: %q", res.Status)
	}
	if called != 1 {
		t.Errorf("expected 1 callback call, got %d", called)
	}
}

func TestRunner_ForceRecheck_NilCallbackErrs(t *testing.T) {
	r := Runner{Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c4", Action: "force_recheck"})
	if res.Status != "err" {
		t.Errorf("expected err, got %q", res.Status)
	}
}

func TestRunner_TunnelToggle_ForcesFreshReportAfterSuccess(t *testing.T) {
	var forced int
	var seen []string
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			seen = append(seen, append([]string{name}, args...)...)
			return []byte("ok"), nil
		},
		ForceRecheck: func(ctx context.Context) { forced++ },
		Now:          mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "toggle",
		Action: "tunnel_disable",
		Args:   map[string]any{"ndms_name": "Wireguard3"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if forced != 1 {
		t.Fatalf("ForceRecheck calls = %d, want 1", forced)
	}
	if got := strings.Join(seen, " "); !strings.Contains(got, "interface Wireguard3 down") {
		t.Fatalf("exec = %q", got)
	}
}

func TestRunner_TunnelToggle_DoesNotForceFreshReportAfterFailure(t *testing.T) {
	var forced int
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("ndmc failed"), errors.New("exit status 122")
		},
		ForceRecheck: func(ctx context.Context) { forced++ },
		Now:          mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "toggle",
		Action: "tunnel_enable",
		Args:   map[string]any{"ndms_name": "Wireguard0"},
	})

	if res.Status != "err" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if forced != 0 {
		t.Fatalf("ForceRecheck calls = %d, want 0", forced)
	}
}

func TestRunner_TunnelToggleRejectsUnsafeNDMSName(t *testing.T) {
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("Exec must not be called for unsafe ndms_name: %s %v", name, args)
			return nil, nil
		},
		Now: mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "toggle-unsafe",
		Action: "tunnel_enable",
		Args:   map[string]any{"ndms_name": "Wireguard1; system reboot"},
	})

	if res.Status != "err" || !strings.Contains(res.Output, "ndms_name") {
		t.Fatalf("expected ndms_name validation error, got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_TunnelRestart_DownThenUpAndForcesFreshReport(t *testing.T) {
	var forced int
	var steps []string
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			steps = append(steps, strings.Join(append([]string{name}, args...), " "))
			return []byte("ok"), nil
		},
		ForceRecheck: func(ctx context.Context) { forced++ },
		Sleep: func(ctx context.Context, d time.Duration) error {
			steps = append(steps, "sleep "+d.String())
			return nil
		},
		Now: mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "restart",
		Action: "tunnel_restart",
		Args:   map[string]any{"ndms_name": "Wireguard3"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{
		"ndmc -c interface Wireguard3 down",
		"sleep 1s",
		"ndmc -c interface Wireguard3 up",
	}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%v\nwant:\n%v", steps, want)
	}
	if forced != 1 {
		t.Fatalf("ForceRecheck calls = %d, want 1", forced)
	}
	if !strings.Contains(res.Output, "Wireguard3") || !strings.Contains(res.Output, "restarted") {
		t.Fatalf("unexpected output: %q", res.Output)
	}
}

func TestRunner_TunnelRestartRejectsUnsafeNDMSName(t *testing.T) {
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("Exec must not be called for unsafe ndms_name: %s %v", name, args)
			return nil, nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("Sleep must not be called for unsafe ndms_name")
			return nil
		},
		Now: mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "restart-unsafe",
		Action: "tunnel_restart",
		Args:   map[string]any{"ndms_name": "Wireguard1; system reboot"},
	})

	if res.Status != "err" || !strings.Contains(res.Output, "ndms_name") {
		t.Fatalf("expected ndms_name validation error, got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_TunnelRestart_DoesNotBringUpAfterInterruptedSettleDelay(t *testing.T) {
	var forced int
	var commands []string
	r := Runner{
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, strings.Join(append([]string{name}, args...), " "))
			return []byte("ok"), nil
		},
		Sleep:        func(ctx context.Context, d time.Duration) error { return context.Canceled },
		ForceRecheck: func(ctx context.Context) { forced++ },
		Now:          mockNow(),
	}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "restart-cancel",
		Action: "tunnel_restart",
		Args:   map[string]any{"ndms_name": "Wireguard3"},
	})

	if res.Status != "err" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if strings.Join(commands, "\n") != "ndmc -c interface Wireguard3 down" {
		t.Fatalf("commands = %+v, want only down", commands)
	}
	if forced != 0 {
		t.Fatalf("ForceRecheck calls = %d, want 0", forced)
	}
}

func TestRunner_TunnelDelete_DeletesByCheckNameAndForcesFreshReport(t *testing.T) {
	var deletedID string
	var forced int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		deletedID = r.URL.Query().Get("id")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	})

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		ForceRecheck: func(ctx context.Context) {
			forced++
		},
		Now: mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args:   map[string]any{"check_name": "tunnel_awg13", "ndms_name": "Wireguard3"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if deletedID != "awg13" {
		t.Fatalf("deleted id = %q, want awg13", deletedID)
	}
	if forced != 1 {
		t.Fatalf("ForceRecheck calls = %d, want 1", forced)
	}
}

func TestRunner_TunnelDelete_DisablesDefaultRouteBeforeDeleteAndVerifiesGone(t *testing.T) {
	var calls []string
	getCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "awg10" {
			t.Fatalf("get id=%q", r.URL.Query().Get("id"))
		}
		getCalls++
		if getCalls == 1 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"old","status":"stopped","enabled":true,"defaultRoute":true,"interfaceName":"opkgtun10"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"code":"NOT_FOUND"}`))
	})
	for _, path := range []string{"/api/control/toggle-default-route", "/api/control/toggle-enabled", "/api/tunnels/delete"} {
		path := path
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			calls = append(calls, path+"?id="+r.URL.Query().Get("id"))
			_, _ = w.Write([]byte(`{"success":true}`))
		})
	}

	r := Runner{AwgClient: awgmgrFake(t, mux), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args:   map[string]any{"tunnel_id": "awg10"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{
		"/api/control/toggle-default-route?id=awg10",
		"/api/control/toggle-enabled?id=awg10",
		"/api/tunnels/delete?id=awg10",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls=\n%s\nwant=\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunner_TunnelDelete_ErrsWhenTunnelStillExists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"old","status":"stopped","enabled":false,"defaultRoute":false,"interfaceName":"nwg0","backend":"nativewg"}}`))
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})

	r := Runner{AwgClient: awgmgrFake(t, mux), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args:   map[string]any{"tunnel_id": "awg10"},
	})

	if res.Status != "err" || !strings.Contains(res.Output, "still exists") {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_TunnelDelete_ForgetsLegacyDisabledTunnel(t *testing.T) {
	getCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "awg10" {
			t.Fatalf("get id=%q", r.URL.Query().Get("id"))
		}
		getCalls++
		if getCalls < 3 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"old","state":"disabled","enabled":false,"defaultRoute":false,"interfaceName":"opkgtun10","backend":"kernel"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"code":"NOT_FOUND"}`))
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	var execCalls []string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		execCalls = append(execCalls, strings.Join(append([]string{name}, args...), " "))
		return []byte("ok"), nil
	}
	sleep := func(context.Context, time.Duration) error { return nil }

	r := Runner{AwgClient: awgmgrFake(t, mux), Exec: exec, Sleep: sleep, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args:   map[string]any{"tunnel_id": "awg10"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{
		"rm -f /opt/etc/awg-manager/tunnels/awg10.json",
		"rm -f /opt/etc/awg-manager/awg10.conf",
		"/opt/etc/init.d/S99awg-manager restart",
	}
	if strings.Join(execCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("exec=\n%s\nwant=\n%s", strings.Join(execCalls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunner_TunnelDelete_ForceLegacyCleanup(t *testing.T) {
	getCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		if getCalls < 3 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"old","enabled":false}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"code":"NOT_FOUND"}`))
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	var execCalls []string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		execCalls = append(execCalls, strings.Join(append([]string{name}, args...), " "))
		return []byte("ok"), nil
	}

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      exec,
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args: map[string]any{
			"tunnel_id":            "awg10",
			"force_legacy_cleanup": true,
		},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if len(execCalls) != 3 || !strings.Contains(execCalls[0], "/opt/etc/awg-manager/tunnels/awg10.json") {
		t.Fatalf("exec calls=%v", execCalls)
	}
}

func TestRunner_TunnelDelete_ForceLegacyCleanupAfterReferencedError(t *testing.T) {
	getCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/get", func(w http.ResponseWriter, r *http.Request) {
		getCalls++
		if getCalls == 1 {
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"awg10","name":"old","enabled":false,"status":"disabled","interfaceName":"nwg0"}}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"code":"NOT_FOUND"}`))
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"tunnel_referenced","details":{"tunnelId":"awg10"}}`))
	})
	var execCalls []string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		execCalls = append(execCalls, strings.Join(append([]string{name}, args...), " "))
		return []byte("ok"), nil
	}

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      exec,
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "del1",
		Action: "tunnel_delete",
		Args: map[string]any{
			"tunnel_id":            "awg10",
			"force_legacy_cleanup": true,
		},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "tunnel_referenced") {
		t.Fatalf("output should retain awgmgr reason, got %q", res.Output)
	}
	if len(execCalls) != 3 || !strings.Contains(execCalls[0], "/opt/etc/awg-manager/tunnels/awg10.json") {
		t.Fatalf("exec calls=%v", execCalls)
	}
}

func TestRunner_OpkgUpgrade_NilRunnerErrs(t *testing.T) {
	r := Runner{Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c5", Action: "opkg_upgrade"})
	if res.Status != "err" {
		t.Errorf("expected err, got %q", res.Status)
	}
}

func TestRunner_OpkgUpgrade_DispatchesToOpkg(t *testing.T) {
	stub := stubOpkg{retStatus: "ok", retOutput: "all packages up to date"}
	r := Runner{Now: mockNow(), Opkg: &stub}
	res := r.Execute(context.Background(), wire.Command{ID: "c5", Action: "opkg_upgrade"})
	if res.Status != "ok" {
		t.Errorf("status: %q", res.Status)
	}
	if !strings.Contains(res.Output, "up to date") {
		t.Errorf("output: %q", res.Output)
	}
	if stub.calls != 1 {
		t.Errorf("expected 1 DryRun call, got %d", stub.calls)
	}
}

func TestRunner_OpkgCronStatusDispatches(t *testing.T) {
	exec := fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt": {out: "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 100000 75000 60% /opt\n"},
		"crontab -l": {out: ""},
	})
	r := Runner{Now: mockNow(), Exec: exec}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "cron-status",
		Action: "opkg_cron_status",
		Args:   map[string]any{"lines": float64(10)},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, `"script_path"`) || !strings.Contains(res.Output, `"free_kb":75000`) {
		t.Fatalf("output=%s", res.Output)
	}
}

func TestLogLinesArgBoundsUntrustedNumbers(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want int
	}{
		{name: "default", args: nil, want: 80},
		{name: "valid float", args: map[string]any{"lines": float64(10)}, want: 10},
		{name: "negative falls back", args: map[string]any{"lines": float64(-1)}, want: 80},
		{name: "too large clamps", args: map[string]any{"lines": float64(10_000)}, want: 300},
		{name: "nan falls back", args: map[string]any{"lines": math.NaN()}, want: 80},
		{name: "positive infinity clamps", args: map[string]any{"lines": math.Inf(1)}, want: 300},
		{name: "huge json number clamps without int overflow", args: map[string]any{"lines": json.Number("999999999999999999")}, want: 300},
		{name: "invalid json number falls back", args: map[string]any{"lines": json.Number("not-a-number")}, want: 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logLinesArg(tt.args); got != tt.want {
				t.Fatalf("logLinesArg()=%d want %d", got, tt.want)
			}
		})
	}
}

func TestRunner_UnknownAction(t *testing.T) {
	r := Runner{Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c6", Action: "frobnicate"})
	if res.Status != "err" {
		t.Errorf("status: %q", res.Status)
	}
}

func TestRunner_RouterDoctor_OK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.10.6","activeBackend":"nativewg","routerIP":"192.168.0.1","firmwareVersion":"5.00.C.11.0-0","totalMemoryMB":489,"singbox":{"installed":true,"version":"1.13.8"}}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"awg11","name":"amnezia","status":"running","enabled":true,"defaultRoute":true,"interfaceName":"nwg1"}]}}`))
	})
	mux.HandleFunc("/api/pingcheck/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[{"tunnelId":"awg11","tunnelName":"amnezia","status":"alive"}]}}`))
	})
	cli := awgmgrFake(t, mux)
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pidof":
			return []byte("123\n"), nil
		case "ip":
			return []byte("1.1.1.1 via 10.0.0.1 dev nwg1 src 10.8.1.2\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command %s", name)
		}
	}
	r := Runner{AwgClient: cli, Exec: exec, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "doctor", Action: "router_doctor"})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	for _, want := range []string{"awg-manager API", "2.10.6", "tunnels", "pingcheck", "wg-monitor agent"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output missing %q:\n%s", want, res.Output)
		}
	}
}

func TestRunner_RouterDoctor_FailsWhenProcessMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.10.6"}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"awg11","status":"running"}]}}`))
	})
	mux.HandleFunc("/api/pingcheck/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":false,"tunnels":[]}}`))
	})
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "pidof" && len(args) > 0 && args[0] == "awg-manager" {
			return nil, fmt.Errorf("not found")
		}
		if name == "pidof" {
			return []byte("123\n"), nil
		}
		return []byte("default via 192.168.0.1 dev br0\n"), nil
	}
	r := Runner{AwgClient: awgmgrFake(t, mux), Exec: exec, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "doctor", Action: "router_doctor"})
	if res.Status != "err" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "awg-manager daemon") || !strings.Contains(res.Output, "not running") {
		t.Errorf("missing process failure:\n%s", res.Output)
	}
}

func TestRunner_HRNeoDoctor_OK(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"hr:yt","name":"YouTube","enabled":true,"backend":"hydraroute","hrPolicyName":"HydraRoute","domains":["youtube.com"],"manualDomains":["10.10.0.0/16"],"routes":[{"interface":"nwg1","tunnelId":"nwg1"}]}]}`))
	})
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "/opt/etc/init.d/S99hrneo":
			return []byte("running"), nil
		case "pidof":
			return []byte("321\n"), nil
		case "ipset":
			return []byte("hrneo_domains\nhrneo_routes\n"), nil
		case "iptables-save":
			return []byte("-A PREROUTING -j NFLOG\n"), nil
		case "opkg":
			return []byte(opkgInfoHrneoGolden), nil
		default:
			return nil, fmt.Errorf("unexpected command %s", name)
		}
	}
	r := Runner{AwgClient: awgmgrFake(t, mux), Exec: exec, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "hrdoc", Action: "hrneo_doctor"})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	for _, want := range []string{"HR-Neo Doctor", "installed/running", "rules: 1", "ipset", "NFLOG", "2.4.0"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("missing %q in:\n%s", want, res.Output)
		}
	}
}

type stubOpkg struct {
	calls     int
	retStatus string
	retOutput string
	retErr    error
	smartFn   func(ctx context.Context) (string, string, wire.OpkgUpgradeResult)
	disableFn func(ctx context.Context, url string) (string, string, wire.OpkgUpgradeResult)
}

func (s *stubOpkg) DryRun(ctx context.Context) (status, output string) {
	s.calls++
	if s.retErr != nil {
		return "err", s.retErr.Error()
	}
	return s.retStatus, s.retOutput
}

func (s *stubOpkg) SmartUpgrade(ctx context.Context) (status, output string, payload wire.OpkgUpgradeResult) {
	s.calls++
	if s.smartFn != nil {
		return s.smartFn(ctx)
	}
	if s.retErr != nil {
		return "err", s.retErr.Error(), payload
	}
	return s.retStatus, s.retOutput, payload
}

func (s *stubOpkg) DisableFeed(ctx context.Context, url string) (status, output string, payload wire.OpkgUpgradeResult) {
	if s.disableFn != nil {
		return s.disableFn(ctx, url)
	}
	return "ok", "", payload
}

// Sanity: errors.Is plumbing works for opkg run errors when unwrapping.
func TestRunner_PreservesAction(t *testing.T) {
	cli := awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "preserve", Action: "pingcheck_now"})
	if res.ID != "preserve" {
		t.Errorf("id should be preserved verbatim, got %q", res.ID)
	}
}

// base64 of a minimal valid .conf with Address field
const testConfB64 = "W0ludGVyZmFjZV0KUHJpdmF0ZUtleSA9IEFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE9CkFkZHJlc3MgPSAxMC45OS4wLjIvMzIKSmMgPSA0CkptaW4gPSA0MApKbWF4ID0gNzAKUzEgPSAwClMyID0gMApIMSA9IDExMTExMTExMTEKSDIgPSAyMjIyMjIyMjIyCkgzID0gMzMzMzMzMzMzMwpINCA9IDQwMDAwMDAwMDAKCltQZWVyXQpQdWJsaWNLZXkgPSBCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCQkJCPQpFbmRwb2ludCA9IHZwbi5leGFtcGxlLmNvbTo1MTgyMApBbGxvd2VkSVBzID0gMC4wLjAuMC8wCg=="

func TestRunner_TunnelImport_CreateAndReplace(t *testing.T) {
	tunnelsAllResp := `{"success":true,"data":{"tunnels":[{"id":"old-id","name":"awg11","defaultRoute":true,"enabled":true}],"external":[],"system":[]}}`
	replaceResp := `{"success":true,"data":{"id":"old-id","name":"awg11","type":"awg","status":"running","enabled":true,"defaultRoute":true}}`
	hydroResp := `{"success":true,"data":{"installed":false,"running":false}}`

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tunnelsAllResp))
	})
	mux.HandleFunc("/api/tunnels/replace", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "old-id" {
			t.Errorf("replace: wrong id %q", r.URL.Query().Get("id"))
		}
		var body struct {
			Backend string `json:"backend"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Backend != "nativewg" {
			t.Fatalf("replace must request nativewg backend, got %q", body.Backend)
		}
		w.Write([]byte(replaceResp))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hydroResp))
	})
	cli := awgmgrFake(t, mux)
	r := Runner{AwgClient: cli, Exec: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	}, Sleep: func(context.Context, time.Duration) error { return nil }, Now: mockNow()}

	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp1",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "awg11", "replace": true},
	})
	if res.Status != "ok" {
		t.Errorf("status=%q output=%q", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "awg11") {
		t.Errorf("output missing tunnel name: %q", res.Output)
	}
}

func TestRunner_TunnelImport_TreatsRunningWithoutHandshakeAsStarted(t *testing.T) {
	var startCalls int
	var sleepCalls int
	var allCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		allCalls++
		switch {
		case allCalls <= 2:
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"old-id","name":"awg11","defaultRoute":true,"enabled":true}],"external":[],"system":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"old-id","name":"awg11","type":"awg","status":"running","enabled":true,"defaultRoute":true,"interfaceName":"nwg1"}],"external":[],"system":[]}}`))
		}
	})
	mux.HandleFunc("/api/tunnels/replace", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"old-id","name":"awg11","type":"awg","status":"starting","enabled":true,"defaultRoute":true}}`))
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		startCalls++
		if r.URL.Query().Get("id") != "old-id" {
			t.Errorf("start id=%q, want old-id", r.URL.Query().Get("id"))
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":false,"running":false}}`))
	})

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      func(ctx context.Context, name string, args ...string) ([]byte, error) { return []byte("ok"), nil },
		Sleep: func(ctx context.Context, d time.Duration) error {
			sleepCalls++
			return nil
		},
		Now: mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp-verify",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "awg11", "replace": true},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if startCalls != 1 {
		t.Fatalf("start calls=%d, want 1", startCalls)
	}
	if sleepCalls == 0 {
		t.Fatal("expected import verification to wait before final status")
	}
	for _, want := range []string{"status=running", "handshake=none"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("output missing %q:\n%s", want, res.Output)
		}
	}
}

func TestRunner_TunnelImport_ReplaceFallsBackToMatchingAddress(t *testing.T) {
	tunnelsAllResp := `{"success":true,"data":{"tunnels":[{"id":"legacy-nl","name":"nl","address":"10.99.0.2/32","defaultRoute":true,"enabled":false}],"external":[],"system":[]}}`
	replaceResp := `{"success":true,"data":{"id":"legacy-nl","name":"amnezia_nl","type":"awg","status":"running","enabled":true,"defaultRoute":true}}`
	hydroResp := `{"success":true,"data":{"installed":false,"running":false}}`
	var replacedID string
	var imported bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tunnelsAllResp))
	})
	mux.HandleFunc("/api/tunnels/replace", func(w http.ResponseWriter, r *http.Request) {
		replacedID = r.URL.Query().Get("id")
		w.Write([]byte(replaceResp))
	})
	mux.HandleFunc("/api/import/conf", func(w http.ResponseWriter, r *http.Request) {
		imported = true
		w.WriteHeader(http.StatusTeapot)
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hydroResp))
	})

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp-address",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": true},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if replacedID != "legacy-nl" {
		t.Fatalf("replace id = %q, want legacy-nl", replacedID)
	}
	if imported {
		t.Fatal("ImportConf should not be called when address fallback matches")
	}
}

func TestRunner_TunnelImport_CreatesProviderConfigsWithNativeWGBackend(t *testing.T) {
	var importBackend string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"old","name":"old","backend":"kernel"}],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/import/conf", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Backend string `json:"backend"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		importBackend = body.Backend
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"new-id","name":"amnezia_nl","type":"awg","status":"running","enabled":false,"defaultRoute":true}}`))
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":false,"running":false}}`))
	})

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp-nativewg",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": false},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if importBackend != "nativewg" {
		t.Fatalf("provider config imports must request nativewg backend, got %q", importBackend)
	}
}

func TestRunner_TunnelImport_RecreatesKernelTunnelAsNativeWG(t *testing.T) {
	var deletedID string
	var importBackend string
	var replaceCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[{"id":"old-kernel","name":"amnezia_nl","backend":"kernel","enabled":true}],"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/tunnels/replace", func(w http.ResponseWriter, r *http.Request) {
		replaceCalled = true
		w.WriteHeader(http.StatusTeapot)
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		deletedID = r.URL.Query().Get("id")
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/import/conf", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Backend string `json:"backend"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		importBackend = body.Backend
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"new-native","name":"amnezia_nl","type":"awg","status":"running","enabled":false,"defaultRoute":true,"backend":"nativewg"}}`))
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":false,"running":false}}`))
	})

	r := Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "imp-kernel-nativewg",
		Action: "tunnel_import",
		Args:   map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": true, "backend": "nativewg"},
	})

	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if replaceCalled {
		t.Fatal("kernel tunnel must not be replaced in-place")
	}
	if deletedID != "old-kernel" {
		t.Fatalf("deleted id=%q, want old-kernel", deletedID)
	}
	if importBackend != "nativewg" {
		t.Fatalf("recreated tunnel backend=%q, want nativewg", importBackend)
	}
}

func TestRunner_TunnelImport_MissingArgs(t *testing.T) {
	r := Runner{AwgClient: awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{
		ID: "imp2", Action: "tunnel_import",
		Args: map[string]any{},
	})
	if res.Status != "err" {
		t.Errorf("expected err, got %q", res.Status)
	}
}

// Make sure errors package import is used to avoid unused-import lint
var _ = errors.New
var _ = fmt.Sprint

// --- M4.1 service_restart / firmware_* / version_audit tests ---

func TestRunner_ServiceRestart_Hrneo(t *testing.T) {
	var seen [][]string
	r := &Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{name}, args...))
		return []byte("hrneo restart ok"), nil
	}}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "hrneo"},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{"/opt/etc/init.d/S99hrneo", "restart"}
	if len(seen) != 1 || !equalStrSlice(seen[0], want) {
		t.Errorf("exec=%v, want %v", seen, want)
	}
}

func TestRunner_ServiceRestart_Awgmgr(t *testing.T) {
	var seen [][]string
	r := &Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{name}, args...))
		return []byte("awgmgr restart ok"), nil
	}}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "awgmgr"},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{"/opt/etc/init.d/S99awg-manager", "restart"}
	if len(seen) != 1 || !equalStrSlice(seen[0], want) {
		t.Errorf("exec=%v, want %v", seen, want)
	}
}

func TestRunner_ServiceRestart_Router_Disabled(t *testing.T) {
	r := &Runner{AllowRouterReboot: false, Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("Exec must NOT be called when AllowRouterReboot=false")
		return nil, nil
	}}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "router"},
	})
	if res.Status != "err" || !strings.Contains(res.Output, "disabled") {
		t.Errorf("expected disabled error; got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_ServiceRestart_Router_Allowed(t *testing.T) {
	var seen [][]string
	r := &Runner{AllowRouterReboot: true, Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{name}, args...))
		return []byte("reboot scheduled"), nil
	}}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "router"},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{"ndmc", "-c", "system reboot"}
	if len(seen) != 1 || !equalStrSlice(seen[0], want) {
		t.Errorf("exec=%v, want %v", seen, want)
	}
}

func TestRunner_ServiceRestart_UnknownName(t *testing.T) {
	r := &Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("Exec must NOT be called for unknown service")
		return nil, nil
	}}
	res := r.Execute(context.Background(), wire.Command{
		ID: "1", Action: "service_restart",
		Args: map[string]any{"name": "wat"},
	})
	if res.Status != "err" || !strings.Contains(res.Output, "unknown service") {
		t.Errorf("expected unknown service error; got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_FirmwareInstall_Disabled(t *testing.T) {
	r := &Runner{AllowFirmwareInstall: false, Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("Exec must NOT be called when AllowFirmwareInstall=false")
		return nil, nil
	}}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "firmware_install"})
	if res.Status != "err" || !strings.Contains(res.Output, "disabled") {
		t.Errorf("expected disabled error; got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_FirmwareInstall_Allowed(t *testing.T) {
	var seen [][]string
	r := &Runner{AllowFirmwareInstall: true, Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		seen = append(seen, append([]string{name}, args...))
		return []byte("ok"), nil
	}}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "firmware_install"})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	want := []string{"ndmc", "-c", "components commit"}
	if len(seen) != 1 || !equalStrSlice(seen[0], want) {
		t.Errorf("exec=%v, want %v", seen, want)
	}
}

func TestRunner_FirmwareStatus_OK(t *testing.T) {
	r := &Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// only ndmc components list expected
		return []byte(ndmcComponentsListGolden_NoUpdate), nil
	}}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "firmware_status"})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	var fs wire.FirmwareStatus
	if err := json.Unmarshal([]byte(res.Output), &fs); err != nil {
		t.Fatalf("output not valid JSON FirmwareStatus: %v\noutput=%q", err, res.Output)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q", fs.Current)
	}
}

func TestRunner_VersionAudit_NoAwgClient(t *testing.T) {
	r := &Runner{Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }}
	res := r.Execute(context.Background(), wire.Command{ID: "1", Action: "version_audit"})
	if res.Status != "err" || !strings.Contains(res.Output, "awgmgr client not configured") {
		t.Errorf("status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_OpkgFeedDisable_Dispatch(t *testing.T) {
	called := ""
	r := &Runner{
		Opkg: &stubOpkg{
			disableFn: func(ctx context.Context, url string) (string, string, wire.OpkgUpgradeResult) {
				called = url
				return "ok", "🔧 Отключён", wire.OpkgUpgradeResult{Output: "🔧 Отключён"}
			},
		},
		Now: mockNow(),
	}
	cmd := wire.Command{
		ID:     "c1",
		Action: "opkg_feed_disable",
		Args:   map[string]any{"url": "https://x/Packages.gz"},
	}
	res := r.Execute(context.Background(), cmd)
	if res.Status != "ok" {
		t.Errorf("status=%q", res.Status)
	}
	if called != "https://x/Packages.gz" {
		t.Errorf("DisableFeed called with url=%q", called)
	}
	if len(res.Payload) == 0 {
		t.Error("res.Payload should be non-empty (OpkgUpgradeResult.IsZero() was false)")
	}
}

func TestRunner_OpkgFeedDisable_MissingURL(t *testing.T) {
	r := &Runner{Opkg: &stubOpkg{}, Now: mockNow()}
	cmd := wire.Command{
		ID:     "c1",
		Action: "opkg_feed_disable",
		Args:   map[string]any{},
	}
	res := r.Execute(context.Background(), cmd)
	if res.Status != "err" {
		t.Errorf("status=%q, want err", res.Status)
	}
	if !strings.Contains(res.Output, "url") {
		t.Errorf("output should mention missing url: %q", res.Output)
	}
}

func equalStrSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- Task 5: diag_now auto-trigger + poll loop tests ---

// awgmgrFakeState backs a per-path fake server for diag_now auto-trigger
// tests. Each call counts toward resultHits / runHits; the bodies are
// supplied by callbacks so each test customises behaviour over hits.
type awgmgrFakeState struct {
	resultHits int
	runHits    int
	resultBody func(hit int) (status int, body string)
	runBody    func(hit int) (status int, body string)
}

func awgmgrFakeMulti(t *testing.T, state *awgmgrFakeState) *awgmgr.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/diagnostics/result":
			state.resultHits++
			s, b := state.resultBody(state.resultHits)
			w.WriteHeader(s)
			_, _ = w.Write([]byte(b))
		case "/api/diagnostics/run":
			state.runHits++
			s, b := state.runBody(state.runHits)
			w.WriteHeader(s)
			_, _ = w.Write([]byte(b))
		default:
			t.Errorf("unexpected path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &awgmgr.Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
}

// newRunnerForTest constructs a Runner with the given awgmgr client and
// poll-tuning fields set for fast unit-test execution.
func newRunnerForTest(t *testing.T, cli *awgmgr.Client, pollEvery time.Duration, pollMax int) *Runner {
	t.Helper()
	return &Runner{
		AwgClient:     cli,
		Now:           mockNow(),
		DiagPollEvery: pollEvery,
		DiagPollMax:   pollMax,
	}
}

func TestRunner_DiagNow_NoReport_TriggersAndPolls(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(hit int) (int, string) {
			if hit < 3 {
				return 400, `{"error":true,"code":"NO_REPORT"}`
			}
			return 200, `{"system":{"appVersion":"2.8.2"}}`
		},
		runBody: func(_ int) (int, string) {
			return 200, `{"success":true,"data":{"status":"running"}}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := newRunnerForTest(t, cli, 10*time.Millisecond, 12)

	res, err := r.DiagNow(context.Background())
	if err != nil {
		t.Fatalf("DiagNow: %v", err)
	}
	if !strings.Contains(res, "2.8.2") {
		t.Errorf("expected final result body, got: %q", res)
	}
	if state.runHits != 1 {
		t.Errorf("expected exactly 1 run call, got %d", state.runHits)
	}
	if state.resultHits < 3 {
		t.Errorf("expected at least 3 result polls, got %d", state.resultHits)
	}
}

func TestRunner_DiagNow_ImmediateOK_NoTrigger(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 200, `{"system":{"appVersion":"2.8.2"}}`
		},
		runBody: func(_ int) (int, string) {
			t.Errorf("DiagRun should NOT be called when result is immediately OK")
			return 200, ""
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := newRunnerForTest(t, cli, 10*time.Millisecond, 12)

	if _, err := r.DiagNow(context.Background()); err != nil {
		t.Fatalf("DiagNow: %v", err)
	}
	if state.runHits != 0 {
		t.Errorf("expected 0 run calls, got %d", state.runHits)
	}
}

func TestRunner_DiagNow_NoReport_TimeoutEmitsTypedToken(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 400, `{"code":"NO_REPORT"}` // never resolves
		},
		runBody: func(_ int) (int, string) {
			return 200, `{"success":true,"data":{"status":"running"}}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := newRunnerForTest(t, cli, 5*time.Millisecond, 3) // tight cap
	_, err := r.DiagNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DIAG_TIMEOUT") {
		t.Errorf("expected DIAG_TIMEOUT, got: %v", err)
	}
}

func TestRunner_DiagNow_NoReport_RunFails_BubblesError(t *testing.T) {
	state := &awgmgrFakeState{
		resultBody: func(_ int) (int, string) {
			return 400, `{"code":"NO_REPORT"}`
		},
		runBody: func(_ int) (int, string) {
			return 503, `{"error":true,"message":"awgmgr restarting"}`
		},
	}
	cli := awgmgrFakeMulti(t, state)
	r := newRunnerForTest(t, cli, 5*time.Millisecond, 12)
	_, err := r.DiagNow(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP_503") {
		t.Errorf("expected HTTP_503 bubble, got: %v", err)
	}
}

func TestRunner_PingCheckStatus_Dispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pingcheck/status":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"success":true,"data":{"enabled":true,"tunnels":[]}}`))
		case "/api/tunnels/all":
			// Non-fatal for enrichment; 500 exercises the graceful-degrade path.
			w.WriteHeader(500)
		default:
			t.Errorf("unexpected path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL), Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "pingcheck_status"})
	if res.Status != "ok" {
		t.Fatalf("status: %s output=%s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "tunnels") {
		t.Errorf("expected tunnels in output: %s", res.Output)
	}
}

func TestRunner_PingCheckToggle_Dispatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	exec := ExecFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		t.Errorf("primary should win, ndmc not called")
		return nil, nil
	})
	r := &Runner{AwgClient: awgmgr.New(srv.URL), Exec: exec, Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{
		ID: "y", Action: "pingcheck_toggle",
		Args: map[string]any{"tunnel_id": "awg10", "ndms_name": "Wireguard0", "enable": false},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output=%s", res.Status, res.Output)
	}
}

func TestRunner_PingCheckToggle_MissingArgs(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused"), Exec: ExecFunc(func(ctx context.Context, name string, a ...string) ([]byte, error) { return nil, nil }), Now: time.Now}
	res := r.Execute(context.Background(), wire.Command{ID: "z", Action: "pingcheck_toggle", Args: map[string]any{}})
	if res.Status != "err" {
		t.Errorf("expected err on missing args, got %s", res.Status)
	}
}

func TestRunner_PingCheckToggleRejectsUnsafeNDMSName(t *testing.T) {
	r := &Runner{
		AwgClient: awgmgr.New("http://unused"),
		Exec: ExecFunc(func(ctx context.Context, name string, a ...string) ([]byte, error) {
			t.Fatalf("Exec must not run with unsafe ndms_name: %s %v", name, a)
			return nil, nil
		}),
		Now: time.Now,
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "pingcheck-unsafe",
		Action: "pingcheck_toggle",
		Args:   map[string]any{"tunnel_id": "awg10", "ndms_name": "Wireguard0; system reboot", "enable": false},
	})
	if res.Status != "err" || !strings.Contains(res.Output, "ndms_name") {
		t.Fatalf("expected ndms_name validation error, got status=%q output=%q", res.Status, res.Output)
	}
}

func TestRunner_TunnelRestartByIDUsesControlEndpoint(t *testing.T) {
	var gotPath, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotID = r.URL.Path, r.URL.Query().Get("id")
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	// opkg-туннель не имеет ndms_name вовсе: ndmc для него невозможен, и
	// раньше мини-апп получал 400 "unknown_tunnel" на существующий туннель.
	res := r.Execute(context.Background(), wire.Command{
		ID:     "c1",
		Action: "tunnel_restart",
		Args:   map[string]any{"tunnel_id": "awg11"},
	})
	if res.Status != "ok" {
		t.Fatalf("status = %q, out = %q", res.Status, res.Output)
	}
	if gotPath != "/api/control/restart" || gotID != "awg11" {
		t.Errorf("path=%q id=%q", gotPath, gotID)
	}
}

func TestRunner_TunnelRestartFallsBackToNDMCOn404(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var cmds []string
	r := &Runner{
		AwgClient: awgmgr.New(srv.URL),
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cmds = append(cmds, strings.Join(args, " "))
			return []byte("ok"), nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "c2",
		Action: "tunnel_restart",
		Args:   map[string]any{"tunnel_id": "awg20", "ndms_name": "Wireguard0"},
	})
	if res.Status != "ok" {
		t.Fatalf("status = %q, out = %q", res.Status, res.Output)
	}
	// Старая сборка без /api/control/restart -- откат на ndmc, а не отказ.
	// hits == 1 proves awg-manager was actually contacted (and answered 404)
	// before ndmc ran; against the pre-fix code, which never called
	// awg-manager at all, hits would be 0 and this assertion would fail.
	if hits != 1 {
		t.Errorf("awg-manager hits = %d, want 1 (contacted before ndmc fallback)", hits)
	}
	if len(cmds) != 2 || !strings.Contains(cmds[0], "Wireguard0 down") || !strings.Contains(cmds[1], "Wireguard0 up") {
		t.Errorf("ndmc fallback commands = %+v", cmds)
	}
}

func TestRunner_TunnelRestartDoesNotFallBackOnNon404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unknown_tunnel"}`))
	}))
	defer srv.Close()

	var execCalls int
	r := &Runner{
		AwgClient: awgmgr.New(srv.URL),
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			execCalls++
			return []byte("ok"), nil
		},
		Sleep: func(ctx context.Context, d time.Duration) error { return nil },
	}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "c4",
		Action: "tunnel_restart",
		Args:   map[string]any{"tunnel_id": "awg20", "ndms_name": "Wireguard0"},
	})
	// A 400 means the endpoint exists and rejected the input -- a real
	// failure the operator must see, never a reason to fall back to ndmc.
	if res.Status != "err" {
		t.Fatalf("status = %q, out = %q, want err", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "400") || !strings.Contains(res.Output, "unknown_tunnel") {
		t.Errorf("output = %q, want it to carry the 400 body through", res.Output)
	}
	if execCalls != 0 {
		t.Errorf("Exec called %d times, want 0: a non-404 must never fall back to ndmc", execCalls)
	}
}

func TestRunner_TunnelRestartRejectsUnsafeTunnelID(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://127.0.0.1:1")}
	res := r.Execute(context.Background(), wire.Command{
		ID:     "c3",
		Action: "tunnel_restart",
		Args:   map[string]any{"tunnel_id": "awg11; reboot"},
	})
	if res.Status != "err" || !strings.Contains(res.Output, "tunnel_id") {
		t.Errorf("status=%q out=%q", res.Status, res.Output)
	}
}
