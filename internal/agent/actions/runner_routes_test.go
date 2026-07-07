package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestRunner_RouteStatus_Dispatch(t *testing.T) {
	srv := fakeAwgmgrStatus(t, false)
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_status"})
	if res.Status != "ok" {
		t.Fatalf("status: %s, output: %s", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, `"tunnels":`) {
		t.Errorf("output not JSON snapshot: %s", res.Output)
	}
	var snap wire.RouteSnapshot
	if err := json.Unmarshal([]byte(res.Output), &snap); err != nil {
		t.Errorf("output not decodable: %v", err)
	}
}

func TestRunner_RouteRebind_Dispatch(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_rebind",
		Args: map[string]any{"src_tunnel_id": "t1", "dst_tunnel_id": "t2"},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output: %s", res.Status, res.Output)
	}
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		t.Errorf("output not JSON: %v", err)
	}
}

func TestRunner_RouteRebind_PartialFailureStatus(t *testing.T) {
	mock := newRebindMock(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/dns-routes/update" && r.URL.Query().Get("id") == "ndms:Yandex" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		mock.handler().ServeHTTP(w, r)
	}))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}

	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_rebind",
		Args: map[string]any{"src_tunnel_id": "t1", "dst_tunnel_id": "t2"},
	})
	if res.Status != "partial" {
		t.Fatalf("status=%q, want partial; output: %s", res.Status, res.Output)
	}
	var rb wire.RouteRebindResult
	if err := json.Unmarshal([]byte(res.Output), &rb); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if rb.DNS.Failed == 0 {
		t.Fatalf("expected DNS failure in payload, got %+v", rb.DNS)
	}
}

func TestRunner_RouteRebind_MissingArgs(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused.invalid")}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_rebind"})
	if res.Status != "err" {
		t.Errorf("expected err, got %s", res.Status)
	}
}

// routeAddMux returns a mux serving the fixed sequence of awg-manager calls
// RouteAddJSON needs to create a DNS route bound to eth3. refreshOK controls
// whether the post-create /api/routing/refresh call succeeds; when it does
// not, RouteAddJSON still returns "ok" (err == nil) but with a non-empty
// wire.RouteApplyResult.Warning.
func routeAddMux(refreshOK bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"wan-eth3","name":"ISP","iface":"eth3","type":"wan","status":"up","available":true}]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	refreshCalls := 0
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		// The first refresh call is the pre-create refresh RouteAddJSON always
		// issues; only the post-change refresh (2nd call) should reflect
		// refreshOK, so the pre-create step never blocks the create itself.
		if refreshCalls == 1 || refreshOK {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"refresh failed"}`))
	})
	return mux
}

func TestRunner_RouteAdd_Dispatch(t *testing.T) {
	srv := httptest.NewServer(routeAddMux(true))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_add",
		Args: map[string]any{"kind": "dns", "name": "ru", "tunnel_id": "eth3", "targets": []string{"gosuslugi.ru"}},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output: %s", res.Status, res.Output)
	}
	var apply wire.RouteApplyResult
	if err := json.Unmarshal([]byte(res.Output), &apply); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if apply.Warning != "" {
		t.Fatalf("expected no warning on clean add, got %+v", apply)
	}
}

func TestRunner_RouteAdd_PartialFailureStatus(t *testing.T) {
	srv := httptest.NewServer(routeAddMux(false))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_add",
		Args: map[string]any{"kind": "dns", "name": "ru", "tunnel_id": "eth3", "targets": []string{"gosuslugi.ru"}},
	})
	if res.Status != "partial" {
		t.Fatalf("status=%q, want partial; output: %s", res.Status, res.Output)
	}
	var apply wire.RouteApplyResult
	if err := json.Unmarshal([]byte(res.Output), &apply); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if apply.Warning == "" {
		t.Fatalf("expected non-empty warning in payload, got %+v", apply)
	}
}

// routeDeleteMux returns a mux serving the fixed sequence of awg-manager calls
// RouteDeleteJSON needs to delete DNS route "dns1". refreshOK controls whether
// the post-delete /api/routing/refresh call succeeds.
func routeDeleteMux(refreshOK bool) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"dns1","name":"YouTube","domains":["youtube.com"],"manualDomains":["youtube.com"],"enabled":true,"backend":"ndms","routes":[{"interface":"nwg5","tunnelId":"Wireguard3"}]}]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		if refreshOK {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":false,"message":"refresh failed"}`))
	})
	return mux
}

func TestRunner_RouteDelete_Dispatch(t *testing.T) {
	srv := httptest.NewServer(routeDeleteMux(true))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_delete",
		Args: map[string]any{"kind": "dns", "route_id": "dns1"},
	})
	if res.Status != "ok" {
		t.Fatalf("status: %s output: %s", res.Status, res.Output)
	}
	var apply wire.RouteApplyResult
	if err := json.Unmarshal([]byte(res.Output), &apply); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if apply.Warning != "" {
		t.Fatalf("expected no warning on clean delete, got %+v", apply)
	}
}

func TestRunner_RouteDelete_PartialFailureStatus(t *testing.T) {
	srv := httptest.NewServer(routeDeleteMux(false))
	defer srv.Close()
	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{
		ID: "x", Action: "route_delete",
		Args: map[string]any{"kind": "dns", "route_id": "dns1"},
	})
	if res.Status != "partial" {
		t.Fatalf("status=%q, want partial; output: %s", res.Status, res.Output)
	}
	var apply wire.RouteApplyResult
	if err := json.Unmarshal([]byte(res.Output), &apply); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if apply.Warning == "" {
		t.Fatalf("expected non-empty warning in payload, got %+v", apply)
	}
}
