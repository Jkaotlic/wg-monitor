package alerts

import (
	"testing"
	"time"
)

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

func mkArgs(t *testing.T, build func(*SmartReplyArgs)) SmartReplyArgs {
	t.Helper()
	a := SmartReplyArgs{Nickname: "x"}
	build(&a)
	return a
}

func TestClassifyState_OfflineWhenReportStale(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) { a.LastReportAge = 6 * time.Minute })
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("got %v want offline", got)
	}
}

func TestClassifyState_HardWhenIncident(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 30 * time.Second
		a.ActiveIncidents = []IncidentView{{CheckName: "tunnel_awg11", FailCount: 5}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}

func TestClassifyState_DegradedHandshakeBoundary(t *testing.T) {
	cases := []struct {
		age   int
		state SmartReplyState
	}{
		{0, StateOK},
		{59, StateOK},
		{60, StateDegraded},
		{179, StateDegraded},
		// 180+ would normally be a HARD via FSM, but here we test the gap
		// between thresholds: handshake age 180 with no active incident
		// is the unusual "FSM hasn't ticked yet" race — treat as Degraded.
		{180, StateDegraded},
	}
	for _, c := range cases {
		t.Run(time.Duration(c.age).String(), func(t *testing.T) {
			a := mkArgs(t, func(a *SmartReplyArgs) {
				a.LastReportAge = 10 * time.Second
				a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: c.age, PingStatus: "ok"}}
			})
			if got := ClassifyState(a); got != c.state {
				t.Errorf("age=%d got %v want %v", c.age, got, c.state)
			}
		})
	}
}

func TestClassifyState_DegradedPingFails(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 30, PingStatus: "ok", FailCount: 2, FailThresh: 5}}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_OKBaseline(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 5 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"}}
	})
	if got := ClassifyState(a); got != StateOK {
		t.Errorf("got %v want ok", got)
	}
}

func TestClassifyState_MobileLongerStaleWindow(t *testing.T) {
	// Mobile users have a 60-min OFFLINE grace window in heartbeat config,
	// but for smart-reply context we still want to surface "offline" as soon
	// as a report >5 min is stale — that's the user-facing definition.
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 6 * time.Minute
		a.IsMobile = true
	})
	if got := ClassifyState(a); got != StateOffline {
		t.Errorf("mobile: got %v want offline", got)
	}
}

func TestClassifyState_MultiTunnelOnlyOneDegraded(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{
			{Name: "amnezia", HandshakeAge: 12, PingStatus: "ok"},
			{Name: "secondary", HandshakeAge: 120, PingStatus: "ok"},
		}
	})
	if got := ClassifyState(a); got != StateDegraded {
		t.Errorf("got %v want degraded", got)
	}
}

func TestClassifyState_HardWinsOverDegradedHandshake(t *testing.T) {
	a := mkArgs(t, func(a *SmartReplyArgs) {
		a.LastReportAge = 10 * time.Second
		a.Tunnels = []TunnelView{{Name: "amnezia", HandshakeAge: 120, PingStatus: "ok"}}
		a.ActiveIncidents = []IncidentView{{CheckName: "dns"}}
	})
	if got := ClassifyState(a); got != StateHard {
		t.Errorf("got %v want hard", got)
	}
}
