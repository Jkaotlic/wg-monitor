package alerts

import "testing"

func TestSmartReplyStateValues(t *testing.T) {
	if StateOK == StateDegraded || StateDegraded == StateHard || StateHard == StateOffline {
		t.Errorf("states must be distinct")
	}
}

func TestSmartReplyArgsBuilderSmoke(t *testing.T) {
	a := SmartReplyArgs{
		Nickname:      "vasya",
		Tunnels:       []TunnelView{{Name: "amnezia", Interface: "nwg0", HandshakeAge: 47, PingStatus: "ok", Latency: 12}},
		LastReportAge: 0,
	}
	if a.Nickname != "vasya" || len(a.Tunnels) != 1 || a.Tunnels[0].HandshakeAge != 47 {
		t.Errorf("smoke: args build: %+v", a)
	}
}
