package actions

import (
	"context"
	"encoding/json"
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

func TestRunner_RouteRebind_MissingArgs(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused.invalid")}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_rebind"})
	if res.Status != "err" {
		t.Errorf("expected err, got %s", res.Status)
	}
}
