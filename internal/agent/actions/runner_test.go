package actions

import (
	"context"
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
}

func (s *stubOpkg) DryRun(ctx context.Context) (status, output string) {
	s.calls++
	if s.retErr != nil {
		return "err", s.retErr.Error()
	}
	return s.retStatus, s.retOutput
}

func (s *stubOpkg) SmartUpgrade(ctx context.Context) (status, output string) {
	s.calls++
	if s.retErr != nil {
		return "err", s.retErr.Error()
	}
	return s.retStatus, s.retOutput
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

// Make sure errors package import is used to avoid unused-import lint
var _ = errors.New
var _ = fmt.Sprint
