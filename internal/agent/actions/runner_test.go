package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
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

func TestRunner_UnknownAction(t *testing.T) {
	r := Runner{Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "c6", Action: "frobnicate"})
	if res.Status != "err" {
		t.Errorf("status: %q", res.Status)
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
		w.Write([]byte(replaceResp))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hydroResp))
	})
	cli := awgmgrFake(t, mux)
	r := Runner{AwgClient: cli, Exec: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, nil
	}, Now: mockNow()}

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
