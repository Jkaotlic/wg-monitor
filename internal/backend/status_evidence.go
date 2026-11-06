package backend

import (
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

const statusEvidenceFreshFor = 5 * time.Minute

// statusEvidence is the backend-owned explanation for the current traffic
// status. Screens may choose different copy, but they must not independently
// upgrade this verdict: every source, timestamp and confidence lives here.
type statusEvidence struct {
	State          string                 `json:"state"`
	ReasonCode     string                 `json:"reason_code"`
	ObservedAt     string                 `json:"observed_at,omitempty"`
	FreshUntil     string                 `json:"fresh_until,omitempty"`
	Sources        []statusEvidenceSource `json:"sources"`
	Confidence     string                 `json:"confidence"`
	AffectedRoutes int                    `json:"affected_routes"`
}

type statusEvidenceSource struct {
	Source     string `json:"source"`
	State      string `json:"state"`
	ObservedAt string `json:"observed_at,omitempty"`
}

func deriveStatusEvidence(tunnels []miniappTunnel, traffic miniappTraffic, now time.Time) statusEvidence {
	base := statusEvidence{
		State:      "unknown",
		ReasonCode: "carrier_unknown",
		Confidence: "none",
		Sources:    []statusEvidenceSource{},
	}
	carrier := newestTunnelByID(tunnels, traffic.CarrierTunnelID)
	if carrier == nil {
		return base
	}

	base.AffectedRoutes = carrier.RoutesDNS + carrier.RoutesStatic
	base.Sources = []statusEvidenceSource{
		{Source: "bot_check", State: carrier.Status, ObservedAt: carrier.TS},
		{Source: "router", State: carrier.RunState, ObservedAt: carrier.TS},
		{Source: "awg_manager", State: awgmEvidenceState(carrier), ObservedAt: carrier.TS},
	}
	observed, err := time.Parse(time.RFC3339, carrier.TS)
	if err != nil {
		base.ReasonCode = "evidence_time_missing"
		return base
	}
	observed = observed.UTC()
	if observed.After(now.UTC().Add(2 * time.Minute)) {
		base.ObservedAt = observed.Format(time.RFC3339)
		base.ReasonCode = "clock_skew"
		return base
	}
	freshUntil := observed.Add(statusEvidenceFreshFor)
	base.ObservedAt = observed.Format(time.RFC3339)
	base.FreshUntil = freshUntil.Format(time.RFC3339)
	if now.UTC().After(freshUntil) {
		base.State = "stale"
		base.ReasonCode = "evidence_stale"
		return base
	}

	botUp := carrier.Status == "ok"
	botDown := carrier.Status == "fail" || !traffic.CarrierAlive
	awgmUp := carrier.AwgmConn == "connected"
	awgmDown := carrier.AwgmConn == "disconnected"
	if (botUp && awgmDown) || (botDown && awgmUp) {
		base.State = "degraded"
		base.ReasonCode = "source_conflict"
		base.Confidence = "medium"
		return base
	}
	if botDown {
		base.State = "failed"
		base.ReasonCode = "carrier_failed"
		base.Confidence = "high"
		return base
	}
	if !botUp {
		base.ReasonCode = "bot_check_unknown"
		base.Confidence = "low"
		return base
	}
	if carrier.AwgmConn == "" || carrier.AwgmConn == "error" {
		base.State = "degraded"
		base.ReasonCode = "source_unverified"
		base.Confidence = "medium"
		return base
	}
	if awgmUp && (carrier.RunState == "running" || carrier.RunState == "up") {
		base.State = "healthy"
		base.ReasonCode = "sources_agree"
		base.Confidence = "high"
		return base
	}
	base.ReasonCode = "router_state_unknown"
	base.Confidence = "low"
	return base
}

// deriveStatusEvidenceFromChecks covers routing modes that deliberately have
// no single carrier. In sing-box mode a destination chooses its own path; in
// direct mode there is no VPN carrier at all. Calling either one
// "carrier_unknown" would turn a valid topology into a false warning, so the
// fresh HydraRoute verdict is the evidence for those modes.
func deriveStatusEvidenceFromChecks(tunnels []miniappTunnel, traffic miniappTraffic, checks map[string]db.EventRow, now time.Time) statusEvidence {
	if traffic.Mode != miniappTrafficSingbox && traffic.Mode != miniappTrafficDirect {
		base := deriveStatusEvidence(tunnels, traffic, now)
		if traffic.CarrierBasis != miniappCarrierPolicy {
			return base
		}
		row, ok := checks["hydraroute"]
		if !ok || row.TS.IsZero() {
			base.State, base.ReasonCode, base.Confidence = "unknown", "routing_evidence_missing", "none"
			return base
		}
		observed := row.TS.UTC()
		base.Sources = append(base.Sources, statusEvidenceSource{Source: "hydraroute", State: row.Status, ObservedAt: observed.Format(time.RFC3339)})
		if observed.After(now.UTC().Add(2 * time.Minute)) {
			base.State, base.ReasonCode, base.Confidence = "unknown", "clock_skew", "none"
			return base
		}
		if now.UTC().After(observed.Add(statusEvidenceFreshFor)) {
			base.State, base.ReasonCode, base.Confidence = "stale", "routing_evidence_stale", "none"
			base.ObservedAt = observed.Format(time.RFC3339)
			base.FreshUntil = observed.Add(statusEvidenceFreshFor).Format(time.RFC3339)
			return base
		}
		if row.Status == "fail" {
			base.State, base.ReasonCode, base.Confidence = "failed", "route_control_failed", "high"
			return base
		}
		if row.Status != "ok" {
			base.State, base.ReasonCode, base.Confidence = "unknown", "route_control_unknown", "low"
			return base
		}
		if tunnelObserved, err := time.Parse(time.RFC3339, base.ObservedAt); err != nil || observed.Before(tunnelObserved) {
			base.ObservedAt = observed.Format(time.RFC3339)
			base.FreshUntil = observed.Add(statusEvidenceFreshFor).Format(time.RFC3339)
		}
		return base
	}
	reason := "address_routed"
	if traffic.Mode == miniappTrafficDirect {
		reason = "direct_path"
	}
	base := statusEvidence{
		State:      "unknown",
		ReasonCode: "routing_evidence_missing",
		Confidence: "none",
		Sources:    []statusEvidenceSource{},
	}
	row, ok := checks["hydraroute"]
	if !ok || row.TS.IsZero() {
		return base
	}
	observed := row.TS.UTC()
	if observed.After(now.UTC().Add(2 * time.Minute)) {
		base.ObservedAt = observed.Format(time.RFC3339)
		base.ReasonCode = "clock_skew"
		return base
	}
	freshUntil := observed.Add(statusEvidenceFreshFor)
	base.ObservedAt = observed.Format(time.RFC3339)
	base.FreshUntil = freshUntil.Format(time.RFC3339)
	base.Sources = []statusEvidenceSource{{
		Source: "hydraroute", State: row.Status, ObservedAt: base.ObservedAt,
	}}
	if now.UTC().After(freshUntil) {
		base.State = "stale"
		base.ReasonCode = "evidence_stale"
		return base
	}
	if row.Status == "fail" {
		base.State = "failed"
		base.ReasonCode = "route_control_failed"
		base.Confidence = "high"
		return base
	}
	if row.Status != "ok" {
		base.ReasonCode = "route_control_unknown"
		base.Confidence = "low"
		return base
	}
	base.State = "healthy"
	base.ReasonCode = reason
	base.Confidence = "high"
	return base
}

func newestTunnelByID(tunnels []miniappTunnel, id string) *miniappTunnel {
	var newest *miniappTunnel
	var newestAt time.Time
	for i := range tunnels {
		if tunnels[i].TunnelID != id {
			continue
		}
		at, err := time.Parse(time.RFC3339, tunnels[i].TS)
		if newest == nil || (err == nil && (newestAt.IsZero() || at.After(newestAt))) {
			newest = &tunnels[i]
			if err == nil {
				newestAt = at
			}
		}
	}
	return newest
}

func awgmEvidenceState(t *miniappTunnel) string {
	if t.AwgmConn != "" {
		return t.AwgmConn
	}
	if t.RunState != "" {
		return "unverified"
	}
	return "unknown"
}
