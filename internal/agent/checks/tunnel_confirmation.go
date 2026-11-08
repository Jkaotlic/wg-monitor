package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TunnelProbeResult contains evidence, not a guess about the VPN provider.
// Target is a fixed label; no router credentials or raw request errors travel.
type TunnelProbeResult struct {
	Target    string `json:"target"`
	State     string `json:"state"` // reachable | unreachable | unknown
	LatencyMs int64  `json:"latency_ms,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

const tunnelConfirmationBudget = 4 * time.Second

// ConfirmTunnel bypasses router DNS and proxy environment. Both the device
// and source address are pinned: failure to bind cannot fall back to WAN.
func ConfirmTunnel(ctx context.Context, tu awgmgr.Tunnel) []TunnelProbeResult {
	targets := []struct{ name, url string }{
		{"cloudflare", "https://1.1.1.1/cdn-cgi/trace"},
		{"google", "https://8.8.8.8/resolve?name=example.com&type=A"},
	}
	out := make([]TunnelProbeResult, len(targets))
	address := strings.TrimSpace(strings.Split(tu.Address, "/")[0])
	source := net.ParseIP(address)
	if strings.TrimSpace(tu.InterfaceName) == "" || source == nil || source.To4() == nil {
		for i, target := range targets {
			out[i] = TunnelProbeResult{Target: target.name, State: "unknown", Reason: "interface_address_missing"}
		}
		return out
	}
	dialer := IfaceDialer(tu.InterfaceName)
	dialer.LocalAddr = &net.TCPAddr{IP: source}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func(i int, name, url string) { defer wg.Done(); out[i] = confirmTunnelTarget(ctx, client, name, url) }(i, target.name, target.url)
	}
	wg.Wait()
	return out
}

func confirmTunnelTarget(ctx context.Context, client *http.Client, name, target string) TunnelProbeResult {
	result := TunnelProbeResult{Target: name, State: "unknown"}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		result.Reason = "invalid_target"
		return result
	}
	resp, err := client.Do(req)
	result.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			result.Reason = "cancelled"
			return result
		}
		var certificateError *tls.CertificateVerificationError
		if errors.As(err, &certificateError) {
			result.Reason = "certificate_validation_failed"
			return result
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENODEV) || errors.Is(err, syscall.EADDRNOTAVAIL) {
			result.Reason = "interface_bind_failed"
			return result
		}
		result.State, result.Reason = "unreachable", "request_failed"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 500 {
		result.State = "reachable"
	} else {
		result.Reason = "target_server_error"
	}
	return result
}

// confirmSuspectTunnels leaves successful/disabled checks alone. Its single
// aggregate deadline bounds work even when a router has many failed tunnels.
func confirmSuspectTunnels(ctx context.Context, checks []wire.Check, tunnels []awgmgr.Tunnel, confirm func(context.Context, awgmgr.Tunnel) []TunnelProbeResult) {
	if confirm == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, tunnelConfirmationBudget)
	defer cancel()
	byID := make(map[string]awgmgr.Tunnel, len(tunnels))
	for _, tu := range tunnels {
		byID[tu.ID] = tu
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for i := range checks {
		id, ok := strings.CutPrefix(checks[i].Name, tunnelCheckPrefix)
		tu, found := byID[id]
		if !ok || !found || !tu.Enabled {
			continue
		}
		// The old matrix heuristic cannot distinguish a shared target outage
		// from shared data-path failure. Check those otherwise OK/unknown
		// results too, while preserving deliberate unused-line suppression.
		if checks[i].Status != "fail" {
			if checks[i].Details["matrix_ok"] != false || checks[i].Details["note"] == "tunnel has no DNS/static routes and pingCheck is disabled" {
				continue
			}
			reason, _ := checks[i].Details["unverified_problems"].(string)
			if reason == "" {
				reason = probeFailedReason
			}
			if !confirmationCanResolve(wire.Check{Details: map[string]any{"error": reason}}) {
				continue
			}
			checks[i].Status = "fail"
			checks[i].Details["error"] = reason
		}
		classifyTunnelFailure(&checks[i])
		if !confirmationCanResolve(checks[i]) {
			continue
		}
		wg.Add(1)
		go func(i int, tu awgmgr.Tunnel) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				applyTunnelConfirmation(&checks[i], tu, nil)
				return
			}
			applyTunnelConfirmation(&checks[i], tu, confirm(ctx, tu))
		}(i, tu)
	}
	wg.Wait()
}

func confirmationCanResolve(c wire.Check) bool {
	reason, _ := c.Details["error"].(string)
	for _, r := range strings.Split(reason, "; ") {
		if r != probeFailedReason && !strings.HasPrefix(r, "handshake stale") && r != "no handshake ever" {
			return false
		}
	}
	return reason != ""
}

func classifyTunnelFailure(c *wire.Check) {
	if c.Details == nil {
		c.Details = map[string]any{}
	}
	reason, _ := c.Details["error"].(string)
	switch {
	case strings.Contains(reason, "address conflict"):
		c.Details["failure_kind"] = "address_conflict"
	case strings.Contains(reason, "status="):
		c.Details["failure_kind"] = "interface"
	case strings.Contains(reason, "handshake"):
		c.Details["failure_kind"] = "handshake"
	case strings.Contains(reason, probeFailedReason):
		c.Details["failure_kind"] = "probe_target"
	}
}

func applyTunnelConfirmation(c *wire.Check, _ awgmgr.Tunnel, results []TunnelProbeResult) {
	classifyTunnelFailure(c)
	c.Details["confirmation_results"] = results
	c.Details["confirmation_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	state := "unknown"
	failed := 0
	for _, r := range results {
		if r.State == "reachable" {
			state = "reachable"
			break
		}
		if r.State == "unreachable" {
			failed++
		}
	}
	if state != "reachable" && len(results) >= 2 && failed == len(results) {
		state = "unreachable"
	}
	c.Details["tunnel_confirmation"] = state
	if !confirmationCanResolve(*c) {
		return
	}
	switch state {
	case "reachable":
		c.Status = "ok"
		delete(c.Details, "error")
		delete(c.Details, "unverified")
		delete(c.Details, "unverified_reason")
		delete(c.Details, "unverified_problems")
		c.Details["failure_kind"] = "probe_target"
		c.Details["note"] = "независимая HTTPS-проверка через VPN-туннель прошла; отказ контрольной пробы не подтверждает обрыв"
	case "unreachable":
		delete(c.Details, "unverified")
		delete(c.Details, "unverified_reason")
		delete(c.Details, "unverified_problems")
		c.Details["failure_kind"] = "data_path"
		c.Details["error"] = "две независимые HTTPS-цели не ответили через VPN-туннель"
	case "unknown":
		if c.Details["failure_kind"] == "probe_target" {
			c.Details["unverified_problems"] = c.Details["error"]
			delete(c.Details, "error")
			c.Status = "ok"
			c.Details["unverified"] = true
			c.Details["unverified_reason"] = "не удалось подтвердить отказ контрольной пробы независимым замером"
			c.Details["failure_kind"] = "confirmation_unavailable"
		}
	}
}
