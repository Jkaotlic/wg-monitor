package actions

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestRunnerExitIPProbe(t *testing.T) {
	changed := true
	var asked string
	r := Runner{Now: mockNow(), ExitProbeNow: func(_ context.Context, id string) (wire.ExitProbe, error) {
		asked = id
		return wire.ExitProbe{VPNIP: "203.0.113.7", DirectIP: "198.51.100.4", Changed: &changed, Source: wire.ExitSourceAwgm, At: time.Now()}, nil
	}}
	res := r.Execute(context.Background(), wire.Command{ID: "e1", Action: "exit_ip_probe", Args: map[string]any{"tunnel_id": " awg11 "}})
	if res.Status != "ok" || asked != "awg11" {
		t.Fatalf("status=%q asked=%q out=%s", res.Status, asked, res.Output)
	}
	var got wire.ExitProbe
	if err := json.Unmarshal([]byte(res.Output), &got); err != nil || got.VPNIP != "203.0.113.7" {
		t.Fatalf("output %q: %v", res.Output, err)
	}
}

func TestRunnerExitIPProbeWithoutProber(t *testing.T) {
	r := Runner{Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "e2", Action: "exit_ip_probe", Args: map[string]any{"tunnel_id": "awg11"}})
	if res.Status != "err" || !strings.Contains(res.Output, "не умеет") {
		t.Fatalf("status=%q out=%q", res.Status, res.Output)
	}
}
