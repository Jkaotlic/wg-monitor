package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeAgents_EmptyLocal_AllAdded(t *testing.T) {
	local := []AgentState{}
	remote := []RemoteAgent{{Nickname: "client-a", SSHHost: "10.0.0.1", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "client-a" || merged[0].Host != "10.0.0.1" {
		t.Fatalf("merged: %+v", merged)
	}
	if !reflect.DeepEqual(added, []string{"client-a"}) {
		t.Fatalf("added: %+v", added)
	}
}

func TestMergeAgents_RemoteOverridesLocal(t *testing.T) {
	local := []AgentState{{Nickname: "client-a", Host: "old", Port: 22, User: "root", Arch: "mips", LastDeployedVersion: "v0.9"}}
	remote := []RemoteAgent{{Nickname: "client-a", SSHHost: "new", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, divergent := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Host != "new" || merged[0].Port != 222 || merged[0].LastDeployedVersion != "v0.10.3" {
		t.Fatalf("merged: %+v", merged)
	}
	if len(added) != 0 {
		t.Fatalf("want 0 added, got %v", added)
	}
	if len(divergent) != 1 || divergent[0] != "client-a" {
		t.Fatalf("want 1 divergent, got %v", divergent)
	}
}

func TestMergeAgents_RemoteNullPreservesLocalSSH(t *testing.T) {
	// Remote has no SSH (NULLs from DB), local has it. Local wins for SSH
	// because remote NULLs are "unknown" not "delete".
	local := []AgentState{{Nickname: "client-a", Host: "192.168.1.1", Port: 222, User: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	remote := []RemoteAgent{{Nickname: "client-a"}} // all empty
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

func TestHeartbeatStatus_Fresh(t *testing.T) {
	ts := time.Now().Add(-30 * time.Second)
	s := formatHeartbeatStatus(&ts, time.Now())
	if !strings.Contains(s, "30") || !strings.Contains(s, "fresh") {
		t.Errorf("want 'fresh ~30s', got %q", s)
	}
}

func TestHeartbeatStatus_Stale(t *testing.T) {
	ts := time.Now().Add(-14 * time.Minute)
	s := formatHeartbeatStatus(&ts, time.Now())
	if !strings.Contains(s, "stale") || !strings.Contains(s, "14") {
		t.Errorf("want 'stale 14m', got %q", s)
	}
}

func TestHeartbeatStatus_Never(t *testing.T) {
	s := formatHeartbeatStatus(nil, time.Now())
	if s != "never" {
		t.Errorf("want 'never', got %q", s)
	}
}
