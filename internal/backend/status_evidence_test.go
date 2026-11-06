package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestStatusEvidenceTreatsFreshSingboxRoutingAsHealthyWithoutInventingCarrier(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	checks := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", TS: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)},
	}

	got := deriveStatusEvidenceFromChecks(nil, miniappTraffic{Mode: miniappTrafficSingbox}, checks, now)

	if got.State != "healthy" || got.ReasonCode != "address_routed" || got.Confidence != "high" {
		t.Fatalf("evidence = %+v", got)
	}
	if len(got.Sources) != 1 || got.Sources[0].Source != "hydraroute" {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

func TestStatusEvidenceTreatsFreshDirectRoutingAsHealthyWithoutCarrier(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	checks := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", TS: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)},
	}

	got := deriveStatusEvidenceFromChecks(nil, miniappTraffic{Mode: miniappTrafficDirect}, checks, now)

	if got.State != "healthy" || got.ReasonCode != "direct_path" || got.Confidence != "high" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestStatusEvidenceRefusesStaleAddressRouting(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 6, 0, 0, time.UTC)
	checks := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", TS: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)},
	}

	got := deriveStatusEvidenceFromChecks(nil, miniappTraffic{Mode: miniappTrafficSingbox}, checks, now)

	if got.State != "stale" || got.ReasonCode != "evidence_stale" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestStatusEvidenceRefusesFutureAddressRoutingClockSkew(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	checks := map[string]db.EventRow{
		"hydraroute": {CheckName: "hydraroute", Status: "ok", TS: now.Add(time.Hour)},
	}
	got := deriveStatusEvidenceFromChecks(nil, miniappTraffic{Mode: miniappTrafficDirect}, checks, now)
	if got.State != "unknown" || got.ReasonCode != "clock_skew" {
		t.Fatalf("future routing evidence was trusted: %+v", got)
	}
}

func TestStatusEvidencePolicyCarrierIsBoundedByHydraRouteFreshness(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 10, 0, 0, time.UTC)
	tunnels := []miniappTunnel{{TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected", TS: now.Add(-time.Minute).Format(time.RFC3339)}}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}
	checks := map[string]db.EventRow{"hydraroute": {CheckName: "hydraroute", Status: "ok", TS: now.Add(-6 * time.Minute)}}

	got := deriveStatusEvidenceFromChecks(tunnels, traffic, checks, now)
	if got.State != "stale" || got.ReasonCode != "routing_evidence_stale" || len(got.Sources) != 4 {
		t.Fatalf("stale policy source was ignored: %+v", got)
	}
}

func TestStatusEvidencePolicyCarrierRequiresHydraRouteEvidence(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	tunnels := []miniappTunnel{{TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected", TS: now.Add(-time.Minute).Format(time.RFC3339)}}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}
	got := deriveStatusEvidenceFromChecks(tunnels, traffic, nil, now)
	if got.State != "unknown" || got.ReasonCode != "routing_evidence_missing" {
		t.Fatalf("missing policy source was ignored: %+v", got)
	}
}

func TestStatusEvidenceFreshAgreementIsVerifiedHealthy(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	tunnels := []miniappTunnel{{
		TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected",
		TS: "2026-11-06T10:00:00Z", RoutesDNS: 10, RoutesStatic: 2,
	}}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}

	got := deriveStatusEvidence(tunnels, traffic, now)

	if got.State != "healthy" || got.ReasonCode != "sources_agree" || got.Confidence != "high" {
		t.Fatalf("evidence = %+v", got)
	}
	if got.ObservedAt != "2026-11-06T10:00:00Z" || got.FreshUntil != "2026-11-06T10:05:00Z" {
		t.Fatalf("times = observed %q fresh %q", got.ObservedAt, got.FreshUntil)
	}
	if got.AffectedRoutes != 12 {
		t.Fatalf("affected routes = %d, want 12", got.AffectedRoutes)
	}
	if len(got.Sources) != 3 {
		t.Fatalf("sources = %+v", got.Sources)
	}
}

func TestStatusEvidenceMakesSourceConflictExplicit(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	tunnels := []miniappTunnel{{
		TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "disconnected",
		TS: "2026-11-06T10:00:00Z",
	}}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}

	got := deriveStatusEvidence(tunnels, traffic, now)

	if got.State != "degraded" || got.ReasonCode != "source_conflict" || got.Confidence != "medium" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestStatusEvidenceRefusesStaleHealthy(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 1, 0, time.UTC)
	tunnels := []miniappTunnel{{
		TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected",
		TS: "2026-11-06T09:55:00Z",
	}}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}

	got := deriveStatusEvidence(tunnels, traffic, now)

	if got.State != "stale" || got.ReasonCode != "evidence_stale" || got.Confidence != "none" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestStatusEvidenceMissingTimestampIsUnknown(t *testing.T) {
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}
	got := deriveStatusEvidence([]miniappTunnel{{TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected"}}, traffic, time.Now())

	if got.State != "unknown" || got.ReasonCode != "evidence_time_missing" || got.Confidence != "none" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestStatusEvidenceRefusesFutureClockSkewAsFreshGreen(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierAlive: true}
	got := deriveStatusEvidence([]miniappTunnel{{TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected", TS: "2026-11-06T11:00:00Z"}}, traffic, now)
	if got.State != "unknown" || got.ReasonCode != "clock_skew" {
		t.Fatalf("future evidence was trusted: %+v", got)
	}
}

func TestStatusEvidenceUsesNewestDuplicateTunnelRow(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 1, 0, 0, time.UTC)
	tunnels := []miniappTunnel{
		{TunnelID: "awg10", Status: "fail", RunState: "broken", AwgmConn: "disconnected", TS: "2026-11-06T09:59:00Z"},
		{TunnelID: "awg10", Status: "ok", RunState: "running", AwgmConn: "connected", TS: "2026-11-06T10:00:00Z"},
	}
	traffic := miniappTraffic{Mode: miniappTrafficVPN, CarrierTunnelID: "awg10", CarrierBasis: miniappCarrierPolicy, CarrierAlive: true}

	got := deriveStatusEvidence(tunnels, traffic, now)

	if got.State != "healthy" || got.ObservedAt != "2026-11-06T10:00:00Z" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestMiniappEventsExposesBackendStatusEvidence(t *testing.T) {
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	now := time.Now().UTC().Truncate(time.Second)
	if err := d.Events().Insert(ownedID, "tunnel_awg10", "ok", `{"tunnel_id":"awg10","tunnel_name":"main","status":"running","routes_dns":7,"active_default_known":true,"is_active_default":true,"awgm_conn":"connected"}`, now); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(ownedID, "hydraroute", "ok", `{"running":true,"routes_hrneo":7,"policies":[{"name":"HydraRoute","dns":7,"via_vpn":true,"active_tunnel_id":"awg10","interfaces":[{"tunnel_id":"awg10","role":"active","via_vpn":true}]}]}`, now); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), nil)
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp miniappRouterEventsResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Evidence.State != "healthy" || resp.Evidence.ReasonCode != "sources_agree" || resp.Evidence.AffectedRoutes != 7 {
		t.Fatalf("evidence = %+v", resp.Evidence)
	}
}
