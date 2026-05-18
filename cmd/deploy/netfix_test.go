package main

import (
	"net"
	"testing"
)

func TestParseNetfixArgsAgentApply(t *testing.T) {
	got, err := parseNetfixArgs([]string{"netfix", "--agent", "alyaba", "--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "alyaba" || !got.Apply {
		t.Fatalf("unexpected opts: %+v", got)
	}
}

func TestParseNetfixArgsRejectsAgentAndHost(t *testing.T) {
	if _, err := parseNetfixArgs([]string{"netfix", "--agent", "a", "--host", "192.168.31.1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNetfixTargetFromAgent(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "alyaba", Host: "192.168.31.1", Port: 22}}}
	host, port, label, err := netfixTarget(state, netfixOptions{Agent: "alyaba"})
	if err != nil {
		t.Fatal(err)
	}
	if host != "192.168.31.1" || port != 22 || label != "alyaba" {
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
