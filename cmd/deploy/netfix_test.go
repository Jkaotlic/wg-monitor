package main

import (
	"net"
	"testing"
)

func TestParseNetfixArgsAgentApply(t *testing.T) {
	got, err := parseNetfixArgs([]string{"netfix", "--agent", "client-a", "--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "client-a" || !got.Apply {
		t.Fatalf("unexpected opts: %+v", got)
	}
}

func TestParseNetfixArgsRejectsAgentAndHost(t *testing.T) {
	if _, err := parseNetfixArgs([]string{"netfix", "--agent", "a", "--host", "192.168.0.1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNetfixTargetFromAgent(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "client-a", Host: "192.168.0.1", Port: 22}}}
	host, port, label, err := netfixTarget(state, netfixOptions{Agent: "client-a"})
	if err != nil {
		t.Fatal(err)
	}
	if host != "192.168.0.1" || port != 22 || label != "client-a" {
		t.Fatalf("unexpected target: host=%s port=%d label=%s", host, port, label)
	}
}

func TestTunnelInterfacesUsesNameHeuristic(t *testing.T) {
	got := tunnelInterfaces([]net.Interface{
		{Index: 20, Name: "Ethernet", Flags: net.FlagUp | net.FlagBroadcast},
		{Index: 46, Name: "wg-srv_legion_laptop", Flags: net.FlagUp | net.FlagBroadcast},
	})
	if len(got) != 1 || got[0].Name != "wg-srv_legion_laptop" {
		t.Fatalf("unexpected tunnels: %+v", got)
	}
}
