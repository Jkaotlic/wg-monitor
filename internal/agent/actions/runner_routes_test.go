package actions

import (
	"context"
	"encoding/json"
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
