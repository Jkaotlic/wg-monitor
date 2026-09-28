package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestFormatHardTunnelAwgmDownSince(t *testing.T) {
	args := HardArgs{
		Nickname: "vasya", CheckName: "tunnel_awg11", ConsecFails: 3,
		HardSince: time.Date(2026, 9, 28, 0, 20, 0, 0, time.UTC),
		Check: wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{
			"tunnel_name": "NL", "handshake_age_sec": 400, "awgm_down_since": "2026-09-28T00:12:00Z",
		}},
	}
	if got := FormatHard(args); !strings.Contains(got, "По awg-manager связь пропадает с 28.09 03:12 МСК") {
		t.Fatalf("нет строки awg-manager:\n%s", got)
	}
	delete(args.Check.Details, "awgm_down_since")
	if got := FormatHard(args); strings.Contains(got, "По awg-manager") {
		t.Fatalf("без пометки агента строки быть не должно:\n%s", got)
	}
}
