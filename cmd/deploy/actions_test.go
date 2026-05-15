package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Regression guard for the client-i scenario: an agent that ended up in
// wizard.toml via [5] VPS Sync (with last_deployed_version="") MUST NOT
// reach the SSH/swap flow when the operator picks [2] Обновить компоненты.
// Without this guard the wizard stops the (non-existent) agent process,
// uploads the binary, then tries to start /opt/etc/init.d/S99wg-monitor
// which doesn't exist → exit 127, router silent. The bail happens BEFORE
// the password prompt, so nil secrets/dl are safe.
func TestActionUpdateAgent_BailsWhenNeverDeployed(t *testing.T) {
	state := &State{
		Agents: []AgentState{{
			Nickname:            "client-i",
			Host:                "192.168.0.1",
			Port:                222,
			User:                "",
			Arch:                "arm64",
			LastDeployedVersion: "",
		}},
	}
	err := actionUpdateAgent(state, nil, nil, "client-i")
	if err == nil {
		t.Fatal("expected error for never-deployed agent, got nil")
	}
	if !strings.Contains(err.Error(), "never deployed") {
		t.Errorf("want error mentioning 'never deployed', got: %v", err)
	}
}

// Asserts the dual-class behaviour: a real, previously-deployed agent
// should NOT be blocked by the never-deployed guard — it still has to
// reach the SSH layer (which is when we'd hit a nil-Downloader panic in
// this hermetic test, so we just check the bail-message isn't the one we
// return). Together with the test above, this pins down exactly which
// agents the new guard catches.
func TestActionUpdateAgent_DoesNotBailWhenDeployedBefore(t *testing.T) {
	state := &State{
		Agents: []AgentState{{
			Nickname:            "testkeen",
			Host:                "192.168.0.1",
			Port:                222,
			User:                "root",
			Arch:                "arm64",
			LastDeployedVersion: "v0.12.0-rc4",
		}},
	}
	defer func() {
		// We're feeding nil dl on purpose: actionUpdateAgent should march
		// past the LastDeployedVersion check and panic on dl.GetLatestRelease.
		// That panic IS the proof the never-deployed guard didn't fire.
		if r := recover(); r == nil {
			t.Fatal("expected nil-dl panic past the never-deployed guard; the guard fired when it shouldn't have")
		}
	}()
	_ = actionUpdateAgent(state, nil, nil, "testkeen")
}

// TestColdInstallGate_DefaultDeniesOnEmpty covers the Layer-2 confirm-gate:
// cold install (ExpectedMAC == "") with default-N answer (operator pressed
// Enter) must bail before any write.
func TestColdInstallGate_DefaultDeniesOnEmpty(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	allowed := coldInstallIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { return "" })
	if allowed {
		t.Fatal("default empty answer must deny; got allow")
	}
}

func TestColdInstallGate_AllowsExplicitYes(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	allowed := coldInstallIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { return "y" })
	if !allowed {
		t.Fatal("explicit y must allow")
	}
}

func TestColdInstallGate_SkipsWhenMACAlreadyPinned(t *testing.T) {
	ag := &AgentState{Nickname: "smith", ExpectedMAC: "aabbccddeeff"}
	called := false
	allowed := coldInstallIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { called = true; return "" })
	if !allowed {
		t.Fatal("with ExpectedMAC pinned, gate is bypassed (Layer 1 + verifyExpectedMAC do the job)")
	}
	if called {
		t.Fatal("ask should not be called when gate is bypassed")
	}
}

func TestColdInstallGate_BypassedByYesToAll(t *testing.T) {
	t.Setenv("WG_YES_TO_ALL", "1")
	ag := &AgentState{Nickname: "smith", ExpectedMAC: ""}
	called := false
	allowed := coldInstallIdentityGate(ag, "keenetic", "aabbccddeeff", "arm64",
		func(string, string) string { called = true; return "" })
	if !allowed {
		t.Fatal("WG_YES_TO_ALL=1 must allow")
	}
	if called {
		t.Fatal("ask should not be called under WG_YES_TO_ALL")
	}
}

// These tests exercise the pure-logic helper diagnosisFromReport, not
// diagnoseUnreachable itself (latter does I/O).

func TestDiagnosisFromReport_NoP2P(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "VPN/SSTP") {
		t.Errorf("want hint about VPN/SSTP, got %q", msg)
	}
}

func TestDiagnosisFromReport_P2PUpButTimeouts(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:222", Candidates: []PathCandidate{
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "tun0") || !strings.Contains(msg, "не маршрутизирует") {
		t.Errorf("want hint blaming the SSTP server, got %q", msg)
	}
}

func TestDiagnosisFromReport_RefusedHint(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("dial tcp: connection refused")},
	}}
	msg := diagnosisFromReport(rep, "")
	if !strings.Contains(msg, "порт закрыт") && !strings.Contains(msg, "refused") {
		t.Errorf("want refused-specific hint, got %q", msg)
	}
}

func TestDiagnosisFromReport_FreshHeartbeatBlamesPath(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:222", Candidates: []PathCandidate{
		{Iface: "Ethernet", Kind: PathLAN, Err: errors.New("i/o timeout")},
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "fresh ~47s")
	if !strings.Contains(msg, "fresh") || !strings.Contains(msg, "сетевом пути") {
		t.Errorf("want fresh-heartbeat hint, got %q", msg)
	}
}

func TestDiagnosisFromReport_StaleHeartbeatBlamesRouter(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:222", Candidates: []PathCandidate{
		{Iface: "tun0", Kind: PathP2P, Err: errors.New("i/o timeout")},
	}}
	msg := diagnosisFromReport(rep, "stale 14m")
	if !strings.Contains(msg, "stale") || !strings.Contains(msg, "выключен") {
		t.Errorf("want stale-heartbeat hint, got %q", msg)
	}
}

// cleanupAgentPaths is the deterministic command-builder for the uninstall
// sequence — we test that the right paths show up rather than running them
// against a real SSH.
func TestCleanupAgentPaths_AllArtifacts(t *testing.T) {
	cmds := cleanupAgentPaths()
	wantFragments := []string{
		"S99wg-monitor stop",
		"killall -9 wg-monitor",
		"/opt/bin/wg-monitor",
		"/opt/bin/wg-monitor.bak",
		"/opt/bin/wg-monitor.new",
		"/opt/etc/wg-monitor",
		"/opt/etc/init.d/S99wg-monitor",
		"/opt/var/wg-monitor",
	}
	joined := strings.Join(cmds, "\n")
	for _, w := range wantFragments {
		if !strings.Contains(joined, w) {
			t.Errorf("cleanup commands missing %q. Full:\n%s", w, joined)
		}
	}
}

func TestCleanupAgentPaths_StopBeforeRemove(t *testing.T) {
	cmds := cleanupAgentPaths()
	stopIdx, rmIdx := -1, -1
	for i, c := range cmds {
		if strings.Contains(c, "stop") && stopIdx == -1 {
			stopIdx = i
		}
		if strings.Contains(c, "rm -f /opt/bin/wg-monitor") && rmIdx == -1 {
			rmIdx = i
		}
	}
	if stopIdx == -1 || rmIdx == -1 || stopIdx > rmIdx {
		t.Errorf("expected stop (idx=%d) before rm (idx=%d)", stopIdx, rmIdx)
	}
}

func TestDoubleDeployHint_TriggersWhenSameNicknameOnTwoBoxes(t *testing.T) {
	rep := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	nicks := map[string]string{
		"Ethernet": "smith",
		"tun0":     "smith",
	}
	hit := detectDoubleDeploy(rep, nicks, "smith")
	if !hit {
		t.Fatal("want detectDoubleDeploy=true when both boxes have same nickname")
	}
}

func TestDoubleDeployHint_NoHitWhenOnlyOneBoxHasAgent(t *testing.T) {
	rep := &PathReport{
		Candidates: []PathCandidate{
			{Iface: "Ethernet", Kind: PathLAN, Latency: 5 * time.Millisecond},
			{Iface: "tun0", Kind: PathP2P, Latency: 142 * time.Millisecond},
		},
	}
	nicks := map[string]string{
		"Ethernet": "",
		"tun0":     "smith",
	}
	hit := detectDoubleDeploy(rep, nicks, "smith")
	if hit {
		t.Fatal("want detectDoubleDeploy=false when only target box has the agent")
	}
}
