package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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

func TestBuildUpdateTargetsTreatsLatestPendingAsWaiting(t *testing.T) {
	state := &State{
		Agents: []AgentState{{
			Nickname:            "carvan",
			Kind:                "mobile",
			LastDeployedVersion: "v0.13.0",
			PendingVersion:      "v0.14.0-rc1",
			PendingSince:        "2026-05-19T10:00:00Z",
		}},
	}

	targets := buildUpdateTargets(state, "v0.14.0-rc1")
	if len(targets) != 1 {
		t.Fatalf("targets=%d, want 1", len(targets))
	}
	if targets[0].NeedsUpdate {
		t.Fatalf("pending latest mobile agent must not be queued for another update: %+v", targets[0])
	}
	if !targets[0].PendingCurrent {
		t.Fatalf("pending latest mobile agent must be marked as waiting for wake: %+v", targets[0])
	}
	if got := filterOutdated(targets); len(got) != 0 {
		t.Fatalf("outdated=%d, want 0 for latest pending agent: %+v", len(got), got)
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

func TestRefreshInstalledComponentStateMergesLiveBackendAndAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok","version":"v0.14.0-rc1"}`))
		case "/v1/wizard/agents":
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Fatalf("Authorization=%q, want Bearer tok", got)
			}
			_, _ = w.Write([]byte(`{"agents":[{"nickname":"carvan","kind":"mobile","thread_id":42,"ssh_host":"10.0.0.2","ssh_port":222,"ssh_user":"root","arch":"mips","last_deployed_version":"v0.14.0-rc1","ring":"canary"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	state := &State{
		Backend: BackendState{Domain: srv.URL, LastDeployedVersion: "v0.13.0"},
		Agents: []AgentState{{
			Nickname:            "carvan",
			Kind:                "mobile",
			LastDeployedVersion: "v0.13.0",
			PendingVersion:      "v0.14.0-rc1",
			PendingSince:        "2026-05-19T10:00:00Z",
		}},
	}
	secrets := &SecretStore{disk: map[string]string{"WIZARD_TOKEN": "tok"}}

	summary := refreshInstalledComponentState(state, secrets)

	if !summary.VPSChecked {
		t.Fatalf("VPS sync was not marked checked: %+v", summary)
	}
	if got := state.Backend.LastDeployedVersion; got != "v0.14.0-rc1" {
		t.Fatalf("backend version=%q, want live healthz version", got)
	}
	agent := state.FindAgent("carvan")
	if agent == nil {
		t.Fatal("agent carvan missing after refresh")
	}
	if agent.LastDeployedVersion != "v0.14.0-rc1" {
		t.Fatalf("agent version=%q, want live VPS version", agent.LastDeployedVersion)
	}
	if agent.PendingVersion != "" || agent.PendingSince != "" {
		t.Fatalf("remote empty pending must clear local pending state: %+v", agent)
	}
	if agent.Host != "10.0.0.2" || agent.ThreadID != 42 || agent.Ring != "canary" {
		t.Fatalf("agent live metadata not merged: %+v", agent)
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

func TestPullDeployHeartbeatTimeoutMarksPendingAfterAck(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "home", Kind: "static", LastDeployedVersion: "v0.13.0-rc14"}}}
	target := updateTarget{IsAgent: true, AgentNickname: "home", Kind: "static", LatestVersion: "v0.13.0-rc27"}
	if err := markPendingOnHeartbeatTimeout(state, target, "2026-05-23T09:00:00Z"); err != nil {
		t.Fatalf("post-ack heartbeat timeout should become pending, got err=%v", err)
	}
	ag := state.FindAgent("home")
	if ag.PendingVersion != "v0.13.0-rc27" || ag.PendingSince != "2026-05-23T09:00:00Z" {
		t.Fatalf("pending fields not set: %+v", ag)
	}
	if ag.LastDeployedVersion != "v0.13.0-rc14" {
		t.Fatalf("last confirmed version must stay old until heartbeat flip, got %+v", ag)
	}
}

func TestPullDeployReadinessRejectsStaleStaticBeforeEnqueue(t *testing.T) {
	lastSeen := time.Date(2026, 5, 23, 6, 39, 0, 0, time.UTC)
	now := lastSeen.Add(8 * time.Minute)

	err := pullDeployReadinessFromAgents([]RemoteAgent{{
		Nickname:   "testkeen",
		LastSeenAt: &lastSeen,
	}}, updateTarget{IsAgent: true, AgentNickname: "testkeen", Kind: "static"}, now)

	if err == nil {
		t.Fatal("stale static agent must not be enqueued for pull deploy")
	}
	for _, want := range []string{"VPS heartbeat stale 8m", "не забирает команды", "wizard предложит re-enroll"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q does not contain %q", err.Error(), want)
		}
	}
}

func TestPullDeployReadinessClassifiesStaleStaticAsDeferredCandidate(t *testing.T) {
	lastSeen := time.Date(2026, 5, 23, 6, 39, 0, 0, time.UTC)
	now := lastSeen.Add(8 * time.Minute)

	err := pullDeployReadinessFromAgents([]RemoteAgent{{
		Nickname:   "testkeen",
		LastSeenAt: &lastSeen,
	}}, updateTarget{IsAgent: true, AgentNickname: "testkeen", Kind: "static"}, now)

	if !canDeferAgentUpdateAfterReadinessError(err) {
		t.Fatalf("stale static pull preflight should be eligible for VPS deferred update, got %v", err)
	}
}

func TestPullDeployReadinessAllowsFreshStatic(t *testing.T) {
	lastSeen := time.Date(2026, 5, 23, 6, 39, 0, 0, time.UTC)
	now := lastSeen.Add(30 * time.Second)

	err := pullDeployReadinessFromAgents([]RemoteAgent{{
		Nickname:   "testkeen",
		LastSeenAt: &lastSeen,
	}}, updateTarget{IsAgent: true, AgentNickname: "testkeen", Kind: "static"}, now)

	if err != nil {
		t.Fatalf("fresh static agent should be pull-ready, got %v", err)
	}
}

func TestPullDeployReadinessRejectsFutureHeartbeat(t *testing.T) {
	now := time.Date(2026, 5, 23, 6, 39, 0, 0, time.UTC)
	future := now.Add(30 * time.Minute)

	err := pullDeployReadinessFromAgents([]RemoteAgent{{
		Nickname:   "testkeen",
		LastSeenAt: &future,
	}}, updateTarget{IsAgent: true, AgentNickname: "testkeen", Kind: "static"}, now)

	if err == nil {
		t.Fatal("future heartbeat must not be treated as fresh")
	}
	if strings.Contains(err.Error(), "fresh ~-") {
		t.Fatalf("future heartbeat rendered as fresh: %q", err.Error())
	}
}

func TestPullDeployReadinessAllowsStaleMobilePendingPath(t *testing.T) {
	err := pullDeployReadinessFromAgents(nil, updateTarget{IsAgent: true, AgentNickname: "carvan", Kind: "mobile"}, time.Now())
	if err != nil {
		t.Fatalf("mobile agents may be sleeping and should use pending path, got %v", err)
	}
}

func TestPullDeployNoAckHintCallsOutStaleHeartbeatAndTokenRepair(t *testing.T) {
	lastSeen := time.Date(2026, 5, 23, 6, 39, 0, 0, time.UTC)
	now := lastSeen.Add(17 * time.Minute)
	hint := pullDeployNoAckHintFromAgents([]RemoteAgent{{
		Nickname:   "testkeen",
		LastSeenAt: &lastSeen,
	}}, updateTarget{AgentNickname: "testkeen"}, now)

	for _, want := range []string{"VPS heartbeat stale 17m", "token", "wizard предложит re-enroll"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q does not contain %q", hint, want)
		}
	}
}

func TestPullDeployNoAckHintExplainsMissingAgent(t *testing.T) {
	hint := pullDeployNoAckHintFromAgents(nil, updateTarget{AgentNickname: "ghost"}, time.Now())
	if !strings.Contains(hint, "ghost") || !strings.Contains(hint, "не найден") || !strings.Contains(hint, "AWG Manager URL здесь") {
		t.Fatalf("unexpected missing-agent hint: %q", hint)
	}
}

func TestShouldFallbackToAWGMReinstallBlocksTokenCorruptPullFailure(t *testing.T) {
	err := fmt.Errorf("pull deploy preflight: VPS heartbeat stale 14h - token-corrupt/rassync")
	if shouldFallbackToAWGMReinstall(err) {
		t.Fatal("token-corrupt/stale pull failure must not auto re-enroll through AWG Manager")
	}
}

func TestShouldFallbackToAWGMReinstallAllowsLegacyAgentFailure(t *testing.T) {
	err := fmt.Errorf("self_update unsupported by old agent")
	if !shouldFallbackToAWGMReinstall(err) {
		t.Fatal("non token-corrupt pull failure should still be eligible for AWG reinstall")
	}
}

func TestShouldOfferAWGMReenrollFromPullErrorDetectsRepairHints(t *testing.T) {
	err := fmt.Errorf("агент не подтвердил команду; если в backend journal есть token-not-found, запусти `repair-agent-token --agent alyaba`")
	if !shouldOfferAWGMReenrollFromPullError(err) {
		t.Fatal("repair/token hints from [2] should offer inline AWG Manager re-enroll")
	}
}

func TestShouldOfferAWGMReenrollFromPullErrorIgnoresGenericTimeout(t *testing.T) {
	err := fmt.Errorf("await result: context deadline exceeded")
	if shouldOfferAWGMReenrollFromPullError(err) {
		t.Fatal("generic transport timeout should not become token re-enroll")
	}
}

func TestNeedsBootstrapForSelfUpdateNohupBugBeforeRC32(t *testing.T) {
	if !needsBootstrapForSelfUpdateNohupBug(updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		InstalledVersion: "v0.13.0-rc31",
		LatestVersion:    "v0.13.0-rc32",
	}) {
		t.Fatal("agent below rc32 must not use its broken self_update to reach rc32")
	}
}

func TestNeedsBootstrapForSelfUpdateNohupBugAllowsFixedAgents(t *testing.T) {
	if needsBootstrapForSelfUpdateNohupBug(updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		InstalledVersion: "v0.13.0-rc32",
		LatestVersion:    "v0.13.0-rc33",
	}) {
		t.Fatal("rc32+ agents have the fixed swap launcher and can use pull-flow")
	}
}

func TestNeedsBootstrapForSelfUpdateNohupBugIgnoresOlderTargets(t *testing.T) {
	if needsBootstrapForSelfUpdateNohupBug(updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		InstalledVersion: "v0.13.0-rc14",
		LatestVersion:    "v0.13.0-rc31",
	}) {
		t.Fatal("guard should only activate when the target contains the rc32 fix")
	}
}

func TestRunOneUpdatePromptsAWGMURLForBrokenSelfUpdateAgent(t *testing.T) {
	state := &State{
		Backend: BackendState{Domain: "wg.example.test"},
		Agents: []AgentState{{
			Nickname:            "alyaba",
			LastDeployedVersion: "v0.13.0-rc31",
		}},
	}
	target := updateTarget{
		IsAgent:          true,
		AgentNickname:    "alyaba",
		InstalledVersion: "v0.13.0-rc31",
		LatestVersion:    "v0.13.0-rc34",
	}
	restore := withTestStdin(t, "alyaba.keenetic.pro\n")
	defer restore()
	oldInstall := installAgentAWGMForUpdate
	defer func() { installAgentAWGMForUpdate = oldInstall }()
	called := false
	installAgentAWGMForUpdate = func(gotState *State, _ *SecretStore, _ *Downloader, nickname string) error {
		called = true
		if gotState != state || nickname != "alyaba" {
			t.Fatalf("unexpected install call state=%p nickname=%q", gotState, nickname)
		}
		ag := gotState.FindAgent("alyaba")
		if ag == nil || ag.AWGMURL != "https://alyaba.keenetic.pro" || ag.DeployMode != "awgm" || ag.AWGMAuth != "router-admin" {
			t.Fatalf("AWGM recovery data not filled: %+v", ag)
		}
		return nil
	}

	if err := runOneUpdate(state, &SecretStore{disk: map[string]string{}}, nil, target); err != nil {
		t.Fatalf("runOneUpdate returned error: %v", err)
	}
	if !called {
		t.Fatal("expected AWG Manager reinstall to be called")
	}
}

func TestRunOneUpdatePromptsAWGMURLForNeverDeployedAgent(t *testing.T) {
	state := &State{
		Backend: BackendState{Domain: "wg.example.test"},
		Agents:  []AgentState{{Nickname: "puzirek"}},
	}
	target := updateTarget{
		IsAgent:       true,
		AgentNickname: "puzirek",
		LatestVersion: "v0.13.0-rc34",
	}
	restore := withTestStdin(t, "puzirek.keenetic.pro\n")
	defer restore()
	oldInstall := installAgentAWGMForUpdate
	defer func() { installAgentAWGMForUpdate = oldInstall }()
	called := false
	installAgentAWGMForUpdate = func(gotState *State, _ *SecretStore, _ *Downloader, nickname string) error {
		called = true
		if nickname != "puzirek" {
			t.Fatalf("nickname=%q, want puzirek", nickname)
		}
		ag := gotState.FindAgent("puzirek")
		if ag == nil || ag.AWGMURL != "https://puzirek.keenetic.pro" {
			t.Fatalf("AWGM URL was not prompted/persisted: %+v", ag)
		}
		return nil
	}

	if err := runOneUpdate(state, &SecretStore{disk: map[string]string{}}, nil, target); err != nil {
		t.Fatalf("runOneUpdate returned error: %v", err)
	}
	if !called {
		t.Fatal("expected AWG Manager install to be called")
	}
}

func withTestStdin(t *testing.T, input string) func() {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	return func() {
		os.Stdin = old
		_ = r.Close()
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
