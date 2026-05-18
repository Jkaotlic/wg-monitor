package tg

import (
	"strings"
	"testing"
)

type routeAddPlanFixture struct {
	Request  routeAddRequestFixture
	Route    routeRuleFixture
	Overlaps []routeOverlapFixture
	CanApply bool
	Hash     string
}

type routeAddRequestFixture struct {
	Kind     string
	Name     string
	TunnelID string
	Targets  []string
	UseHRNeo bool
}

type routeDeletePlanFixture struct {
	Route    routeRuleFixture
	Warnings []routeOverlapFixture
	CanApply bool
	Hash     string
}

type routeRuleFixture struct {
	ID      string
	Name    string
	Kind    string
	Backend string
	Enabled bool
	Bind    string
	Targets []routeTargetFixture
}

type routeTargetFixture struct {
	Type  string
	Value string
}

type routeOverlapFixture struct {
	Severity string
	Reason   string
	Existing routeRuleFixture
	Target   routeTargetFixture
}

func TestRouteAddPreviewTextAllowed(t *testing.T) {
	plan := routeAddPlanFixture{
		Request: routeAddRequestFixture{
			Kind:     "dns",
			Name:     "media",
			TunnelID: "awg10",
			Targets:  []string{"example.com", "api.example.com"},
			UseHRNeo: true,
		},
		CanApply: true,
		Hash:     "h1",
	}
	text := RouteAddPreviewText(plan)
	for _, want := range []string{
		"Preview",
		"Type: DNS / HR-Neo",
		"Name: media",
		"Target: awg10",
		"Targets:",
		"- example.com",
		"- api.example.com",
		"Next: confirm to add this route, or cancel.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Blocking overlaps:") {
		t.Fatalf("allowed preview should not render blocking section:\n%s", text)
	}
	if strings.Contains(text, "        ") {
		t.Fatalf("preview should not rely on wide spacing:\n%s", text)
	}
}

func TestRouteAddPreviewKeyboardHidesConfirmWhenBlocked(t *testing.T) {
	plan := routeAddPlanFixture{
		Request: routeAddRequestFixture{Kind: "static", Name: "corp", TunnelID: "awg11"},
		Overlaps: []routeOverlapFixture{{
			Severity: "block",
			Reason:   "10.10.0.0/16 already goes to awg10",
			Target:   routeTargetFixture{Value: "10.10.0.0/16"},
		}},
		CanApply: false,
	}
	text := RouteAddPreviewText(plan)
	if !strings.Contains(text, "Blocking overlaps:") {
		t.Fatalf("blocked preview should render blocking section:\n%s", text)
	}
	kb := RouteAddPreviewKeyboard(42, "draft1", "confirm1", plan)
	if hasCallback(kb, "routes_add_confirm:42:_panel_:draft1:confirm1") {
		t.Fatalf("confirm button should be hidden for blocked add preview: %+v", kb)
	}
	if !hasCallback(kb, "routes_add_cancel:42:_panel_:draft1") {
		t.Fatalf("cancel button missing: %+v", kb)
	}
}

func TestRouteDeletePreviewTextAndKeyboard(t *testing.T) {
	plan := routeDeletePlanFixture{
		Route: routeRuleFixture{
			ID:      "r1",
			Name:    "default",
			Kind:    "static",
			Backend: "awg-manager",
			Enabled: true,
			Bind:    "wan",
			Targets: []routeTargetFixture{{Value: "0.0.0.0/0"}},
		},
		Warnings: []routeOverlapFixture{{Severity: "warn", Reason: "default-route-like target"}},
		CanApply: true,
		Hash:     "h2",
	}
	text := RouteDeletePreviewText(plan)
	for _, want := range []string{
		"Delete preview",
		"Kind: static",
		"Backend: awg-manager",
		"Bind: wan",
		"Targets:",
		"- 0.0.0.0/0",
		"Warnings:",
		"- default-route-like target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	kb := RouteDeletePreviewKeyboard(42, "draft2", "confirm2")
	if !hasCallback(kb, "routes_del_confirm:42:_panel_:draft2:confirm2") {
		t.Fatalf("confirm button missing: %+v", kb)
	}
	if !hasCallback(kb, "routes_del_cancel:42:_panel_:draft2") {
		t.Fatalf("cancel button missing: %+v", kb)
	}
}

func TestRouteApplyResultText(t *testing.T) {
	res := struct {
		Action         string
		Kind           string
		RouteID        string
		RouteName      string
		HRNeoRestarted bool
	}{
		Action:         "add",
		Kind:           "dns",
		RouteID:        "r1",
		RouteName:      "media",
		HRNeoRestarted: true,
	}
	text := RouteApplyResultText(res)
	for _, want := range []string{"Route add complete", "Kind: dns", "Name: media", "ID: r1", "HR-Neo restarted: yes"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}

func hasCallback(kb InlineKeyboardMarkup, callback string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == callback {
				return true
			}
		}
	}
	return false
}
