package tg

import (
	"strings"
	"testing"
)

func TestTunnelsPanelKeyboardAddsDeleteAskButton(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		TunnelID:  "awg13",
		Name:      "amnezia_dead",
		CheckName: "tunnel_awg13",
		NDMSName:  "Wireguard3",
		Enabled:   false,
		Status:    "dead",
	}})

	if !hasTunnelPanelCallback(kb, "tunnel_delete_ask:42:tunnel_awg13:Wireguard3:awg13") {
		t.Fatalf("missing delete ask button: %+v", kb)
	}
}

func TestTunnelsPanelKeyboardAddsDeleteAskButtonWithoutNDMSName(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		TunnelID:  "kernel-10",
		Name:      "kernel_tunnel",
		CheckName: "tunnel_kernel-10",
		Enabled:   true,
		Status:    "running",
	}})

	if !hasTunnelPanelCallback(kb, "tunnel_delete_ask:42:tunnel_kernel-10::kernel-10") {
		t.Fatalf("missing delete ask button for tunnel without NDMSName: %+v", kb)
	}
}

func TestTunnelsPanelKeyboardAddsPerTunnelRestartButton(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		TunnelID:  "awg13",
		Name:      "amnezia_dead",
		CheckName: "tunnel_awg13",
		NDMSName:  "Wireguard3",
		Enabled:   true,
		Status:    "dead",
	}})

	if !hasTunnelPanelCallback(kb, "tunnel_restart:42:tunnel_awg13:Wireguard3") {
		t.Fatalf("missing per-tunnel restart button: %+v", kb)
	}
	if !hasTunnelPanelCallback(kb, "restart_tunnel:42:_panel_") {
		t.Fatalf("global awg-manager restart should stay available: %+v", kb)
	}
}

func TestTunnelsPanelKeyboardSkipsUnsafeButtonsWithoutNDMSName(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		TunnelID:  "awg13",
		Name:      "amnezia_dead",
		CheckName: "tunnel_awg13",
		Enabled:   true,
		Status:    "dead",
	}})

	for _, prefix := range []string{
		"tunnel_disable:42:tunnel_awg13:",
		"tunnel_enable:42:tunnel_awg13:",
		"tunnel_restart:42:tunnel_awg13:",
	} {
		if hasTunnelPanelCallbackPrefix(kb, prefix) {
			t.Fatalf("unsafe per-tunnel callback %q should be hidden without NDMSName: %+v", prefix, kb)
		}
	}
	if !hasTunnelPanelCallback(kb, "restart_tunnel:42:_panel_") {
		t.Fatalf("global awg-manager restart should stay available: %+v", kb)
	}
	if !hasTunnelPanelCallback(kb, "tunnels_refresh:42:_panel_") {
		t.Fatalf("refresh should stay available: %+v", kb)
	}
}

func TestTunnelsPanelKeyboardOffersRoutesAfterStatusCheck(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		Name:      "amnezia_live",
		CheckName: "tunnel_awg13",
		NDMSName:  "Wireguard3",
		Enabled:   true,
		Status:    "running",
	}})

	if !hasTunnelPanelCallback(kb, "routes_open:42:_panel_") {
		t.Fatalf("missing routes next-action button: %+v", kb)
	}
}

func hasTunnelPanelCallback(kb InlineKeyboardMarkup, want string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if btn.CallbackData == want {
				return true
			}
		}
	}
	return false
}

func hasTunnelPanelCallbackPrefix(kb InlineKeyboardMarkup, prefix string) bool {
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix) {
				return true
			}
		}
	}
	return false
}
