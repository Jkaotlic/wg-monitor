package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestBuildUpdateTargetsCarriesMobileRolloutMetadata(t *testing.T) {
	state := &State{
		Agents: []AgentState{{
			Nickname:            "carvan",
			Kind:                "mobile",
			Ring:                "canary",
			LastDeployedVersion: "v0.13.0",
			PendingVersion:      "v0.14.0-rc1",
			PendingSince:        "2026-05-19T10:00:00Z",
		}},
	}
	targets := buildUpdateTargets(state, "v0.14.0-rc1")
	if len(targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(targets))
	}
	got := targets[0]
	if got.Kind != "mobile" || got.Ring != "canary" || got.PendingVersion != "v0.14.0-rc1" {
		t.Fatalf("metadata not carried into update target: %+v", got)
	}
}

func TestRefreshBackendInstalledVersionUsesHealthz(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"v0.13.0-rc12"}`))
	}))
	defer srv.Close()

	state := &State{Backend: BackendState{
		Domain:              srv.URL,
		LastDeployedVersion: "v0.13.0-rc4",
	}}
	refreshBackendInstalledVersion(state)
	if got := state.Backend.LastDeployedVersion; got != "v0.13.0-rc12" {
		t.Fatalf("backend version = %q, want live healthz version", got)
	}
}

func TestDomainHostNormalizesBackendDomain(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"wgmonitor.example", "wgmonitor.example"},
		{"https://wgmonitor.example/healthz", "wgmonitor.example"},
		{"wgmonitor.example:443", "wgmonitor.example"},
	}
	for _, tt := range tests {
		if got := domainHost(tt.in); got != tt.want {
			t.Fatalf("domainHost(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMobilePullAckTimeoutMarksPending(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "carvan", Kind: "mobile"}}}
	target := updateTarget{
		IsAgent:       true,
		AgentNickname: "carvan",
		Kind:          "mobile",
		LatestVersion: "v0.14.0-rc1",
	}
	err := markPendingOnMobileAckTimeout(state, target, "2026-05-19T10:00:00Z")
	if err != nil {
		t.Fatalf("mobile timeout should become pending, got err=%v", err)
	}
	ag := state.FindAgent("carvan")
	if ag.PendingVersion != "v0.14.0-rc1" || ag.PendingSince != "2026-05-19T10:00:00Z" {
		t.Fatalf("pending fields not set: %+v", ag)
	}
}

func TestStaticPullAckTimeoutRemainsError(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "home", Kind: "static"}}}
	target := updateTarget{IsAgent: true, AgentNickname: "home", Kind: "static", LatestVersion: "v0.14.0-rc1"}
	if err := markPendingOnMobileAckTimeout(state, target, "2026-05-19T10:00:00Z"); err == nil {
		t.Fatal("static timeout must remain an error")
	}
}

func TestBackendReleaseAssetURLUsesBackendDomain(t *testing.T) {
	got := backendReleaseAssetURL(&State{Backend: BackendState{Domain: "wg.example.test"}}, "v0.13.0-rc18", "wg-monitor-agent-linux-arm64")
	want := "https://wg.example.test/v1/releases/download/v0.13.0-rc18/wg-monitor-agent-linux-arm64"
	if got != want {
		t.Fatalf("url=%q want %q", got, want)
	}
}

func TestBackendReleaseAssetURLPreservesExplicitScheme(t *testing.T) {
	got := backendReleaseAssetURL(&State{Backend: BackendState{Domain: "http://127.0.0.1:8080/healthz"}}, "v0.13.0-rc18", "checksums.txt")
	want := "http://127.0.0.1:8080/v1/releases/download/v0.13.0-rc18/checksums.txt"
	if got != want {
		t.Fatalf("url=%q want %q", got, want)
	}
}
