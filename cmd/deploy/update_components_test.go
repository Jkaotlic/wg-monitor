package main

import "testing"

func TestShortCommandID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "empty",
			id:   "",
			want: "",
		},
		{
			name: "short",
			id:   "abc123",
			want: "abc123",
		},
		{
			name: "exactly 12",
			id:   "abcdefghijkl",
			want: "abcdefghijkl",
		},
		{
			name: "long",
			id:   "abcdefghijklmnop",
			want: "abcdefghijkl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortCommandID(tt.id); got != tt.want {
				t.Fatalf("shortCommandID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestShouldProbeReachabilityBeforeUpdate_SkipsPullEligibleAgent(t *testing.T) {
	state := &State{Backend: BackendState{Domain: "wg.example.test"}}
	secrets := &SecretStore{disk: map[string]string{"WIZARD_TOKEN": "tok"}}
	target := updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		Host:             "192.168.31.1",
		Port:             222,
		InstalledVersion: "v0.13.0-rc6",
		LatestVersion:    "v0.13.0-rc7",
	}

	if shouldProbeReachabilityBeforeUpdate(state, secrets, target) {
		t.Fatal("pull-eligible agent must not require local TCP reachability before update")
	}
}

func TestShouldProbeReachabilityBeforeUpdate_ProbesAgentWhenPullUnavailable(t *testing.T) {
	state := &State{Backend: BackendState{Domain: "wg.example.test"}}
	secrets := &SecretStore{disk: map[string]string{}}
	target := updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		Host:             "192.168.31.1",
		Port:             222,
		InstalledVersion: "v0.13.0-rc6",
		LatestVersion:    "v0.13.0-rc7",
	}

	if !shouldProbeReachabilityBeforeUpdate(state, secrets, target) {
		t.Fatal("agent without pull prerequisites should still get the SSH reachability preflight")
	}
}

func TestShouldProbeReachabilityBeforeUpdate_SkipsBackend(t *testing.T) {
	target := updateTarget{IsAgent: false, Host: "203.0.113.10", Port: 22}
	if shouldProbeReachabilityBeforeUpdate(&State{}, &SecretStore{}, target) {
		t.Fatal("backend updates should not use the agent LAN TCP preflight")
	}
}
