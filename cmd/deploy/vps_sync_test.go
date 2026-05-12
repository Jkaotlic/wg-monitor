package main

import (
	"reflect"
	"testing"
)

func TestMergeAgents_EmptyLocal_AllAdded(t *testing.T) {
	local := []AgentState{}
	remote := []RemoteAgent{{Nickname: "alyaba", SSHHost: "10.0.0.1", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "alyaba" || merged[0].Host != "10.0.0.1" {
		t.Fatalf("merged: %+v", merged)
	}
	if !reflect.DeepEqual(added, []string{"alyaba"}) {
		t.Fatalf("added: %+v", added)
	}
}

func TestMergeAgents_RemoteOverridesLocal(t *testing.T) {
	local := []AgentState{{Nickname: "alyaba", Host: "old", Port: 22, User: "root", Arch: "mips", LastDeployedVersion: "v0.9"}}
	remote := []RemoteAgent{{Nickname: "alyaba", SSHHost: "new", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, divergent := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Host != "new" || merged[0].Port != 222 || merged[0].LastDeployedVersion != "v0.10.3" {
		t.Fatalf("merged: %+v", merged)
	}
	if len(added) != 0 {
		t.Fatalf("want 0 added, got %v", added)
	}
	if len(divergent) != 1 || divergent[0] != "alyaba" {
		t.Fatalf("want 1 divergent, got %v", divergent)
	}
}

func TestMergeAgents_RemoteNullPreservesLocalSSH(t *testing.T) {
	// Remote has no SSH (NULLs from DB), local has it. Local wins for SSH
	// because remote NULLs are "unknown" not "delete".
	local := []AgentState{{Nickname: "alyaba", Host: "192.168.1.1", Port: 222, User: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	remote := []RemoteAgent{{Nickname: "alyaba"}} // all empty
	merged, _, _ := MergeAgents(local, remote)
	if merged[0].Host != "192.168.1.1" || merged[0].Port != 222 {
		t.Fatalf("local SSH lost: %+v", merged[0])
	}
}

func TestMergeAgents_LocalOnlyKept(t *testing.T) {
	local := []AgentState{{Nickname: "ghost", Host: "1.1.1.1", Port: 22, User: "root", Arch: "mips"}}
	remote := []RemoteAgent{}
	merged, _, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "ghost" {
		t.Fatalf("local-only dropped: %+v", merged)
	}
}
