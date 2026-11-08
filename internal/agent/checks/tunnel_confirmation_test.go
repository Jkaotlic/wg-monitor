package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestConfirmationOneReachableTargetDisprovesProbeOnlyOutage(t *testing.T) {
	c := Fail("tunnel_awg20", time.Now(), probeFailedReason, map[string]any{"matrix_ok": false, "handshake_age_sec": 45})
	tu := awgmgr.Tunnel{ID: "awg20", Enabled: true, Status: "running", InterfaceName: "nwg0"}
	applyTunnelConfirmation(&c, tu, []TunnelProbeResult{{Target: "cloudflare", State: "unreachable"}, {Target: "google", State: "reachable"}})
	if c.Status != "ok" || c.Details["tunnel_confirmation"] != "reachable" || c.Details["matrix_ok"] != false || c.Details["failure_kind"] != "probe_target" {
		t.Fatalf("one working independent destination must disprove outage, preserving raw matrix: %+v", c)
	}
	if _, ok := c.Details["error"]; ok {
		t.Fatal("recovered check retained failure error")
	}
}

func TestConfirmationUnavailableDoesNotClaimRecoveryOrOutage(t *testing.T) {
	c := Fail("tunnel_awg20", time.Now(), probeFailedReason, map[string]any{"matrix_ok": false, "handshake_age_sec": 45})
	applyTunnelConfirmation(&c, awgmgr.Tunnel{Enabled: true, Status: "running"}, []TunnelProbeResult{{State: "unknown"}, {State: "unreachable"}})
	if c.Details["unverified"] != true || c.Details["tunnel_confirmation"] != "unknown" {
		t.Fatalf("unknown evidence must skip FSM: %+v", c)
	}
}

func TestConfirmationBothTargetsFailRetainsFailure(t *testing.T) {
	c := Fail("tunnel_awg20", time.Now(), probeFailedReason, map[string]any{})
	applyTunnelConfirmation(&c, awgmgr.Tunnel{Enabled: true, Status: "running"}, []TunnelProbeResult{{State: "unreachable"}, {State: "unreachable"}})
	if c.Status != "fail" || c.Details["failure_kind"] != "data_path" {
		t.Fatalf("two independent failed destinations: %+v", c)
	}
}

func TestConfirmationNeverClearsStructuralFailure(t *testing.T) {
	for _, reason := range []string{"status=starting; no handshake ever", "address conflict on interface", "pingCheck=dead (fails 3/3)", "handshake timestamp in future (200s)"} {
		t.Run(reason, func(t *testing.T) {
			c := Fail("tunnel_awg20", time.Now(), reason, nil)
			applyTunnelConfirmation(&c, awgmgr.Tunnel{Enabled: true, Status: "running"}, []TunnelProbeResult{{State: "reachable"}})
			if c.Status != "fail" {
				t.Fatalf("must preserve structural fault: %+v", c)
			}
		})
	}
}

func TestConfirmationNoInterfaceNeverUsesDefaultRoute(t *testing.T) {
	results := ConfirmTunnel(context.Background(), awgmgr.Tunnel{})
	for _, r := range results {
		if r.State != "unknown" {
			t.Fatalf("missing interface: %+v", results)
		}
	}
}

func TestConfirmationTargetTimeoutCancellationAndServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	if r := confirmTunnelTarget(context.Background(), server.Client(), "test", server.URL); r.State != "unknown" {
		t.Fatalf("5xx is target failure: %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := confirmTunnelTarget(ctx, server.Client(), "test", server.URL); r.State != "unknown" {
		t.Fatalf("cancellation: %+v", r)
	}
}

func TestConfirmationCollectionIsBoundedAndPreservesDisabledChecks(t *testing.T) {
	checks := []wire.Check{Fail("tunnel_a", time.Now(), probeFailedReason, nil), OK("tunnel_b", time.Now(), nil)}
	tunnels := []awgmgr.Tunnel{{ID: "a", Enabled: true, Status: "running"}, {ID: "b", Enabled: false}}
	calls := 0
	confirmSuspectTunnels(context.Background(), checks, tunnels, func(context.Context, awgmgr.Tunnel) []TunnelProbeResult {
		calls++
		return []TunnelProbeResult{{State: "reachable"}}
	})
	if calls != 1 || checks[0].Status != "ok" || checks[1].Status != "ok" {
		t.Fatalf("calls=%d checks=%+v", calls, checks)
	}
}

func TestTunnelTrafficEvidenceDetectsMovementAndCounterReset(t *testing.T) {
	tracker := &TunnelTrafficHistory{}
	now := time.Now()
	tu := awgmgr.Tunnel{ID: "a", InterfaceName: "nwg0", RxBytes: 100, TxBytes: 20}
	observe := func(at time.Time) map[string]any {
		out := []wire.Check{{Name: "tunnel_a", Details: map[string]any{}}}
		tracker.Annotate(out, []awgmgr.Tunnel{tu}, at)
		return out[0].Details
	}
	if d := observe(now); d["rx_delta_bytes"] != nil {
		t.Fatalf("first sample must not invent traffic: %v", d)
	}
	tu.RxBytes, tu.TxBytes = 160, 35
	d := observe(now.Add(time.Minute))
	if d["rx_delta_bytes"] != int64(60) || d["tx_delta_bytes"] != int64(15) || d["traffic_sample_ms"] != int64(60000) {
		t.Fatalf("wrong traffic delta: %v", d)
	}
	tu.RxBytes, tu.TxBytes = 1, 1
	if d := observe(now.Add(2 * time.Minute)); d["rx_delta_bytes"] != nil || d["traffic_counters_reset"] != true {
		t.Fatalf("restarted counters must not underflow: %v", d)
	}
}

func TestTunnelTrafficEvidenceCannotCrossInterfaceReplacement(t *testing.T) {
	tracker := &TunnelTrafficHistory{}
	out := []wire.Check{{Name: "tunnel_a", Details: map[string]any{}}}
	now := time.Now()
	tracker.Annotate(out, []awgmgr.Tunnel{{ID: "a", InterfaceName: "old", RxBytes: 10}}, now)
	out[0].Details = map[string]any{}
	tracker.Annotate(out, []awgmgr.Tunnel{{ID: "a", InterfaceName: "new", RxBytes: 100}}, now.Add(time.Minute))
	if out[0].Details["rx_delta_bytes"] != nil {
		t.Fatalf("new interface is not previous traffic sample: %v", out[0].Details)
	}
}

func TestConfirmationChecksSharedOrSingleTunnelMatrixFailure(t *testing.T) {
	for _, state := range []string{"unreachable", "unknown", "reachable"} {
		t.Run(state, func(t *testing.T) {
			out := []wire.Check{OK("tunnel_a", time.Now(), map[string]any{"matrix_ok": false, "handshake_age_sec": 30})}
			tu := awgmgr.Tunnel{ID: "a", Enabled: true, Status: "running"}
			calls := 0
			confirmSuspectTunnels(context.Background(), out, []awgmgr.Tunnel{tu}, func(context.Context, awgmgr.Tunnel) []TunnelProbeResult {
				calls++
				return []TunnelProbeResult{{State: state}, {State: state}}
			})
			if calls != 1 {
				t.Fatal("shared failed matrix never independently checked")
			}
			switch state {
			case "unreachable":
				if out[0].Status != "fail" {
					t.Fatalf("confirmed data-path outage ignored: %+v", out[0])
				}
			case "unknown":
				if out[0].Details["unverified"] != true {
					t.Fatalf("unknown evidence claimed healthy: %+v", out[0])
				}
			case "reachable":
				if out[0].Status != "ok" || out[0].Details["unverified"] == true {
					t.Fatalf("working data-path: %+v", out[0])
				}
			}
		})
	}
}

func TestFailedMatrixEvidenceDoesNotNeedHealthyNeighbor(t *testing.T) {
	now := time.Now()
	matrix := &awgmgr.MonitoringMatrix{UpdatedAt: now.UTC().Format(time.RFC3339), Cells: []awgmgr.MatrixCell{{TunnelID: "a", TargetID: "target", OK: false, TS: now.UTC().Format(time.RFC3339)}}}
	if !matrixHasFailedOwnTargets(matrix, "a", now) {
		t.Fatal("failed single-tunnel target evidence discarded")
	}
	matrix.Cells[0].TS = now.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	if matrixHasFailedOwnTargets(matrix, "a", now) {
		t.Fatal("stale failed cells must not trigger fresh confirmation")
	}
}

func TestConfirmationCertificateFailureIsUnknownNotNetworkOutage(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	result := confirmTunnelTarget(context.Background(), client, "untrusted", server.URL)
	if result.State != "unknown" || result.Reason != "certificate_validation_failed" {
		t.Fatalf("local trust/clock fault must not diagnose VPN outage: %+v", result)
	}
}

func TestConfirmationVerifiedFailureRemovesUnknownFSMFlag(t *testing.T) {
	c := Unverified("tunnel_a", time.Now(), "shared target failed", map[string]any{"matrix_ok": false, "unverified_problems": "handshake stale (200s > 180s)"})
	out := []wire.Check{c}
	confirmSuspectTunnels(context.Background(), out, []awgmgr.Tunnel{{ID: "a", Enabled: true, Status: "running"}}, func(context.Context, awgmgr.Tunnel) []TunnelProbeResult {
		return []TunnelProbeResult{{State: "unreachable"}, {State: "unreachable"}}
	})
	if out[0].Status != "fail" || out[0].Details["unverified"] == true {
		t.Fatalf("confirmed outage must reach FSM: %+v", out[0])
	}
}

func TestConfirmationCannotEraseStructuralUnknownProblems(t *testing.T) {
	c := Unverified("tunnel_a", time.Now(), "policy unknown", map[string]any{"matrix_ok": false, "unverified_problems": "status=starting; no handshake ever"})
	out := []wire.Check{c}
	calls := 0
	confirmSuspectTunnels(context.Background(), out, []awgmgr.Tunnel{{ID: "a", Enabled: true, Status: "starting"}}, func(context.Context, awgmgr.Tunnel) []TunnelProbeResult {
		calls++
		return []TunnelProbeResult{{State: "reachable"}}
	})
	if calls != 0 || out[0].Details["unverified"] != true || out[0].Details["unverified_problems"] != "status=starting; no handshake ever" {
		t.Fatalf("structural unknown was cleared: %+v", out[0])
	}
}
