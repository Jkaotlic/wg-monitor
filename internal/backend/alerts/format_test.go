package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestFormatHardGenericFallback(t *testing.T) {
	hardSince := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake", // not a tunnel_*, not dns/hr/awgmgr → generic
		ConsecFails: 3,
		HardSince:   hardSince,
		Check:       wire.Check{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "handshake age 312s > 180s"}},
	})
	for _, want := range []string{"🔴", "vasya", "DOWN", "handshake age 312s", "3 fails подряд"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "📱") {
		t.Fatalf("static user must not have mobile badge:\n%s", got)
	}
}

func TestFormatHardMobileShowsBadge(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname: "carvan", CheckName: "awg_handshake",
		HardSince: time.Now(), IsMobile: true,
		Check: wire.Check{Status: "fail", Details: map[string]any{"error": "x"}},
	})
	if !strings.Contains(got, "📱") {
		t.Fatalf("mobile badge missing:\n%s", got)
	}
}

func TestFormatHardTunnelRichBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:    "vasya",
		CheckName:   "tunnel_awg11",
		ConsecFails: 3,
		HardSince:   time.Date(2026, 4, 29, 14, 30, 0, 0, time.UTC),
		Check: wire.Check{
			Name:   "tunnel_awg11",
			Status: "fail",
			Details: map[string]any{
				"tunnel_name":               "amnezia_for_awg2",
				"interface":                 "nwg0",
				"endpoint":                  "89.125.101.122:37634",
				"isp_interface":             "eth3",
				"handshake_age_sec":         277, // 4m37s
				"ping_check_status":         "dead",
				"ping_check_fail_count":     3,
				"ping_check_fail_threshold": 3,
				"ping_check_restart_count":  2,
				"backend":                   "nativewg",
				"awg_version":               "AWG2.0",
				"mtu":                       1280,
			},
		},
	})
	wants := []string{
		"amnezia_for_awg2", "(nwg0)",
		"🌐", "89.125.101.122:37634", "eth3",
		"🤝 handshake:", "4 мин 37 с",
		"pingCheck", "dead", "3/3",
		"auto-restart 2x",
		"⚙", "nativewg", "AWG AWG2.0", "MTU 1280",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestFormatHardDNSBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "dns",
		HardSince: time.Now(),
		Check: wire.Check{Name: "dns", Status: "fail", Details: map[string]any{
			"endpoints": 4, "failed_count": 0,
			"rkn_probed": 4, "rkn_suspect": 4,
			"error": "RKN block suspected",
		}},
	})
	wants := []string{"DOWN", "🌐 endpoints:", "4 total", "🚫 RKN probe:", "RKN block"}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func TestFormatHardHydraRouteBody(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "hydraroute",
		HardSince: time.Now(),
		Check: wire.Check{Name: "hydraroute", Status: "fail", Details: map[string]any{
			"installed": true, "running": false,
			"error": "installed but not running",
		}},
	})
	if !strings.Contains(got, "📦 HydraRoute") || !strings.Contains(got, "running=false") {
		t.Fatalf("missing hydraroute body:\n%s", got)
	}
}

func TestFormatHardWithNeighbors(t *testing.T) {
	got := FormatHard(HardArgs{
		Nickname:  "vasya",
		CheckName: "tunnel_awg11",
		HardSince: time.Now(),
		Check:     wire.Check{Name: "tunnel_awg11", Status: "fail", Details: map[string]any{"tunnel_name": "main", "interface": "nwg0"}},
		Neighbors: []NeighborSummary{
			{CheckName: "tunnel_awg12", TunnelName: "backup", Interface: "nwg1", Status: "alive", HandshakeAge: 12},
		},
	})
	wants := []string{"Соседи:", "backup", "(nwg1)", "alive", "12с"}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in neighbours block:\n%s", w, got)
		}
	}
}

func TestFormatRecovery(t *testing.T) {
	since := time.Date(2026, 4, 26, 20, 3, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname:    "vasya",
		CheckName:   "awg_handshake",
		HardSince:   since,
		RecoveredAt: since.Add(7 * time.Minute),
	})
	for _, want := range []string{"✅", "vasya", "RECOVERED", "7m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestFormatRouterOffline(t *testing.T) {
	got := FormatRouterOffline("vasya", 11*time.Minute)
	if !strings.Contains(got, "OFFLINE") || !strings.Contains(got, "vasya") || !strings.Contains(got, "11m") {
		t.Fatalf("got: %s", got)
	}
}

func TestFormatRealert(t *testing.T) {
	hardSince := time.Date(2026, 4, 28, 9, 3, 0, 0, time.UTC)
	msg := FormatRealert(RealertArgs{
		Nickname:     "vasya",
		CheckName:    "awg_handshake",
		HardSince:    hardSince,
		RealertCount: 2,
	})
	if !strings.Contains(msg, "STILL DOWN") {
		t.Errorf("missing STILL DOWN: %q", msg)
	}
	if !strings.Contains(msg, "vasya") {
		t.Errorf("missing nickname: %q", msg)
	}
	if !strings.Contains(msg, "awg_handshake") {
		t.Errorf("missing check name: %q", msg)
	}
	if !strings.Contains(msg, "Re-alert #2") {
		t.Errorf("missing re-alert counter: %q", msg)
	}
	if !strings.Contains(msg, "🔁") {
		t.Errorf("missing 🔁 emoji: %q", msg)
	}
}

func TestHumanAgeSec(t *testing.T) {
	cases := map[int]string{
		5:    "5с",
		90:   "1 мин 30 с",
		3600: "1ч 0м",
		7320: "2ч 2м",
	}
	for in, want := range cases {
		if got := humanAgeSec(in); got != want {
			t.Errorf("humanAgeSec(%d) = %q, want %q", in, got, want)
		}
	}
}
