//go:build manual

package actions

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// Ручная сверка снимка с живым роутером. Только чтение.
//
//	AWGM_URL=http://192.168.0.1:2222 go test -tags manual ./internal/agent/actions/ -run LiveRouteSnapshot -v
func TestLiveRouteSnapshot(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	if base == "" {
		t.Skip("AWGM_URL not set")
	}
	out, err := RouteStatus(context.Background(), awgmgr.New(base))
	if err != nil {
		t.Fatalf("RouteStatus: %v", err)
	}
	var pretty any
	if err := json.Unmarshal([]byte(out), &pretty); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	b, _ := json.MarshalIndent(pretty, "", "  ")
	t.Logf("\n%s", b)
}
