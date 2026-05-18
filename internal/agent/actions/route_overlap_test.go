package actions

import (
	"context"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRouteOverlapNormalizeDomain(t *testing.T) {
	got := normalizeRouteTarget(" ExAmPle.COM. ")
	want := wire.RouteTarget{Type: "domain", Value: "example.com"}
	if got != want {
		t.Fatalf("normalizeRouteTarget domain = %+v, want %+v", got, want)
	}
}

func TestRouteOverlapNormalizeIDNADomain(t *testing.T) {
	got := normalizeRouteTarget("B\u00dcCHER.example.")
	want := wire.RouteTarget{Type: "domain", Value: "xn--bcher-kva.example"}
	if got != want {
		t.Fatalf("normalizeRouteTarget IDNA = %+v, want %+v", got, want)
	}
}

func TestRouteOverlapNormalizeCIDRAndIP(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "cidr masks host bits", raw: "10.10.2.7/16", want: "10.10.0.0/16"},
		{name: "ipv4 address", raw: "192.0.2.7", want: "192.0.2.7/32"},
		{name: "ipv6 address", raw: "2001:db8::1", want: "2001:db8::1/128"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeRouteTarget(tt.raw)
			want := wire.RouteTarget{Type: "cidr", Value: tt.want}
			if got != want {
				t.Fatalf("normalizeRouteTarget(%q) = %+v, want %+v", tt.raw, got, want)
			}
		})
	}
}

func TestRouteOverlapDomainExactAndSuffix(t *testing.T) {
	candidate := routeSummary("new", "nwg0", true, "Example.com.")
	existing := []wire.RouteRuleSummary{
		routeSummary("exact", "nwg1", true, "example.com"),
		routeSummary("child", "nwg1", true, "api.example.com"),
	}

	overlaps := classifyRouteOverlaps(candidate, existing)
	if len(overlaps) != 2 {
		t.Fatalf("overlap count = %d, want 2: %+v", len(overlaps), overlaps)
	}
	for _, overlap := range overlaps {
		if overlap.Severity != "block" {
			t.Fatalf("severity = %q, want block: %+v", overlap.Severity, overlap)
		}
		if overlap.Target != (wire.RouteTarget{Type: "domain", Value: "example.com"}) {
			t.Fatalf("target = %+v, want canonical example.com", overlap.Target)
		}
	}
}

func TestRouteOverlapCIDRContainmentAndSameBindWarning(t *testing.T) {
	candidate := routeSummary("new", "nwg0", true, "10.10.2.7")
	existing := []wire.RouteRuleSummary{
		routeSummary("same-bind", "nwg0", true, "10.10.0.0/16"),
	}

	overlaps := classifyRouteOverlaps(candidate, existing)
	if len(overlaps) != 1 {
		t.Fatalf("overlap count = %d, want 1: %+v", len(overlaps), overlaps)
	}
	if overlaps[0].Severity != "warn" {
		t.Fatalf("severity = %q, want warn: %+v", overlaps[0].Severity, overlaps[0])
	}
	if overlaps[0].Target != (wire.RouteTarget{Type: "cidr", Value: "10.10.2.7/32"}) {
		t.Fatalf("target = %+v, want normalized single-IP prefix", overlaps[0].Target)
	}
}

func TestRouteOverlapDifferentBindBlocks(t *testing.T) {
	candidate := routeSummary("new", "nwg0", true, "2001:db8:1::/48")
	existing := []wire.RouteRuleSummary{
		routeSummary("other-bind", "nwg1", true, "2001:db8:1:2::/64"),
	}

	overlaps := classifyRouteOverlaps(candidate, existing)
	if len(overlaps) != 1 {
		t.Fatalf("overlap count = %d, want 1: %+v", len(overlaps), overlaps)
	}
	if overlaps[0].Severity != "block" {
		t.Fatalf("severity = %q, want block: %+v", overlaps[0].Severity, overlaps[0])
	}
}

func TestRouteOverlapDisabledExistingWarns(t *testing.T) {
	candidate := routeSummary("new", "nwg0", true, "blocked.example")
	existing := []wire.RouteRuleSummary{
		routeSummary("disabled", "nwg1", false, "blocked.example"),
	}

	overlaps := classifyRouteOverlaps(candidate, existing)
	if len(overlaps) != 1 {
		t.Fatalf("overlap count = %d, want 1: %+v", len(overlaps), overlaps)
	}
	if overlaps[0].Severity != "warn" {
		t.Fatalf("severity = %q, want warn for disabled existing rule: %+v", overlaps[0].Severity, overlaps[0])
	}
}

func TestRouteOverlapOpaqueInfo(t *testing.T) {
	candidate := routeSummary("new", "nwg0", true, "*.example.com")

	overlaps := classifyRouteOverlaps(candidate, nil)
	if len(overlaps) != 1 {
		t.Fatalf("overlap count = %d, want 1: %+v", len(overlaps), overlaps)
	}
	if overlaps[0].Severity != "info" {
		t.Fatalf("severity = %q, want info: %+v", overlaps[0].Severity, overlaps[0])
	}
	if overlaps[0].Target != (wire.RouteTarget{Type: "opaque", Value: "*.example.com"}) {
		t.Fatalf("target = %+v, want opaque original", overlaps[0].Target)
	}
}

func TestRouteAddPlanRejectsStaticDomainTarget(t *testing.T) {
	req := wire.RouteAddRequest{Kind: "static", Name: "bad", TunnelID: "awg10", Targets: []string{"example.com"}}
	_, err := buildRouteAddPlan(context.Background(), nil, req)
	if err == nil || !strings.Contains(err.Error(), "must be CIDR or IP") {
		t.Fatalf("expected static CIDR validation error, got %v", err)
	}
}

func routeSummary(id, bind string, enabled bool, targets ...string) wire.RouteRuleSummary {
	out := wire.RouteRuleSummary{
		ID:      id,
		Name:    id,
		Kind:    "dns",
		Enabled: enabled,
		Bind:    bind,
	}
	for _, target := range targets {
		out.Targets = append(out.Targets, wire.RouteTarget{Value: target})
	}
	return out
}
