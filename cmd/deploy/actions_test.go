package main

import (
	"strings"
	"testing"
)

// Regression guard for the de4ddy scenario: an agent that ended up in
// wizard.toml via [5] VPS Sync (with last_deployed_version="") MUST NOT
// reach the SSH/swap flow when the operator picks [2] Обновить компоненты.
// Without this guard the wizard stops the (non-existent) agent process,
// uploads the binary, then tries to start /opt/etc/init.d/S99wg-monitor
// which doesn't exist → exit 127, router silent. The bail happens BEFORE
// the password prompt, so nil secrets/dl are safe.
func TestActionUpdateAgent_BailsWhenNeverDeployed(t *testing.T) {
	state := &State{
		Agents: []AgentState{{
			Nickname:            "de4ddy",
			Host:                "192.168.31.1",
			Port:                222,
			User:                "",
			Arch:                "arm64",
			LastDeployedVersion: "",
		}},
	}
	err := actionUpdateAgent(state, nil, nil, "de4ddy")
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
			Host:                "192.168.31.1",
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
