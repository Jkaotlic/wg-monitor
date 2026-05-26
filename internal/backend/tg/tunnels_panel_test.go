package tg

import "testing"

func TestTunnelsPanelKeyboardAddsDeleteAskButton(t *testing.T) {
	kb := TunnelsPanelKeyboard(42, []TunnelPanelEntry{{
		Name:      "amnezia_dead",
		CheckName: "tunnel_awg13",
		NDMSName:  "Wireguard3",
		Enabled:   false,
		Status:    "dead",
	}})

	if !hasTunnelPanelCallback(kb, "tunnel_delete_ask:42:tunnel_awg13:Wireguard3") {
		t.Fatalf("missing delete ask button: %+v", kb)
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
