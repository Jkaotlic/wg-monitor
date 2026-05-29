package main

import (
	"errors"
	"net"
	"os"
	"runtime"
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

func TestAgentsWithSameTargetUsesHostPort(t *testing.T) {
	state := &State{Agents: []AgentState{
		{Nickname: "client-a", Host: "192.168.0.1", Port: 222},
		{Nickname: "client-i", Host: "192.168.0.1", Port: 222},
		{Nickname: "other", Host: "192.168.0.1", Port: 22},
	}}
	got := agentsWithSameTarget(state, &state.Agents[0])
	if strings.Join(got, ",") != "client-a,client-i" {
		t.Fatalf("unexpected peers: %v", got)
	}
}

func TestConfirmAmbiguousTargetRequiresNickname(t *testing.T) {
	state := &State{Agents: []AgentState{
		{Nickname: "client-a", Host: "192.168.0.1", Port: 222},
		{Nickname: "client-i", Host: "192.168.0.1", Port: 222},
	}}
	ok := confirmAmbiguousTarget(state, &state.Agents[0], "keenetic", "eth0=aabbccddeeff", "", "test-op",
		func(string, string) string { return "client-i" })
	if ok {
		t.Fatal("wrong nickname must fail")
	}
	ok = confirmAmbiguousTarget(state, &state.Agents[0], "keenetic", "eth0=aabbccddeeff", "", "test-op",
		func(string, string) string { return "client-a" })
	if !ok {
		t.Fatal("exact nickname must pass")
	}
}

func TestConfirmAmbiguousTargetSkipsUniqueTarget(t *testing.T) {
	state := &State{Agents: []AgentState{{Nickname: "client-a", Host: "192.168.0.1", Port: 222}}}
	called := false
	ok := confirmAmbiguousTarget(state, &state.Agents[0], "", "", "", "test-op",
		func(string, string) string { called = true; return "" })
	if !ok {
		t.Fatal("unique target should pass")
	}
	if called {
		t.Fatal("unique target should not prompt")
	}
}

func TestHashAgentRawTokenMatchesBackendHashShape(t *testing.T) {
	got := hashAgentRawToken("abc")
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Fatalf("hashAgentRawToken mismatch: got %s want %s", got, want)
	}
}

func TestBackendInstallSwapServiceRestartsExistingInstall(t *testing.T) {
	if got := backendInstallSwapService(true); got != "wg-monitor-backend" {
		t.Fatalf("existing backend install should swap through service restart, got %q", got)
	}
	if got := backendInstallSwapService(false); got != "" {
		t.Fatalf("fresh backend install should keep explicit start path, got %q", got)
	}
}

func TestBackendBackupEnableCommandStartsTimer(t *testing.T) {
	got := backendBackupEnableCommand()
	for _, want := range []string{
		"systemctl daemon-reload",
		"systemctl enable --now wg-monitor-backup.timer",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("backup enable command missing %q: %s", want, got)
		}
	}
}

func TestStageAgentRawTokenStoresRecoveryTokenWithoutOverwritingCanonical(t *testing.T) {
	tokenEnv := "WG_AGENT_TOKEN_ALYABA"
	raw := strings.Repeat("a", 64)
	t.Setenv("WG_NO_SECRET_CACHE", "1")
	t.Setenv(tokenEnv, "")
	store := &SecretStore{disk: map[string]string{tokenEnv: strings.Repeat("b", 64)}}

	if err := stageAgentRawToken(store, tokenEnv, raw); err != nil && !errors.Is(err, ErrCacheDisabled) {
		t.Fatalf("stageAgentRawToken returned unexpected error: %v", err)
	}
	if got := os.Getenv(tokenEnv); got != raw {
		t.Fatalf("process env token = %q, want staged raw token", got)
	}
	if got := store.disk[tokenEnv]; got != strings.Repeat("b", 64) {
		t.Fatalf("canonical disk token was overwritten: got %q", got)
	}
	if got := store.disk[tokenEnv+"_STAGED"]; got != raw {
		t.Fatalf("staged disk token = %q, want %q", got, raw)
	}
}

func TestAWGMEnrollmentUsesTwoPhaseForExistingAgent(t *testing.T) {
	store := &SecretStore{disk: map[string]string{"WG_AGENT_TOKEN_TESTKEEN": strings.Repeat("b", 64)}}
	ag := &AgentState{Nickname: "testkeen", LastDeployedVersion: "v0.13.0-rc14"}
	if !awgmEnrollmentNeedsTwoPhase(store, ag) {
		t.Fatal("existing agent with prior deploy/raw token must use two-phase token rotation")
	}
}

func TestAWGMEnrollmentCanCreateDirectForNewAgent(t *testing.T) {
	store := &SecretStore{disk: map[string]string{}}
	ag := &AgentState{Nickname: "newone"}
	if awgmEnrollmentNeedsTwoPhase(store, ag) {
		t.Fatal("brand-new agent may use normal enrollment because no live client can be stranded")
	}
}

func TestNormalizeKeeneticArchMapsAarch64ToReleaseAssetArch(t *testing.T) {
	got, err := normalizeKeeneticArch("aarch64")
	if err != nil {
		t.Fatalf("normalizeKeeneticArch returned error: %v", err)
	}
	if got != "arm64" {
		t.Fatalf("aarch64 must map to release asset arch arm64, got %q", got)
	}
}

func TestApplyAWGMDeploySuccessDoesNotPersistPublicRouterLANIP(t *testing.T) {
	ag := &AgentState{
		Nickname:       "del",
		DeployMode:     "awgm",
		AWGMURL:        "https://awg.delrp.example",
		PendingVersion: "v0.13.0-rc44",
		PendingSince:   "2026-05-26T12:00:00Z",
	}
	applyAWGMDeploySuccess(ag, &AWGMSystemInfo{RouterIP: "192.168.0.1"}, "v0.13.0-rc45", "router-admin", true, time.Unix(1779693600, 0).UTC())

	if ag.Host != "" || ag.Port != 0 || ag.User != "" {
		t.Fatalf("public AWGM install must not store private SSH endpoint, got host=%q port=%d user=%q", ag.Host, ag.Port, ag.User)
	}
	if ag.LastDeployedVersion != "v0.13.0-rc45" || ag.LastDeploy == "" || ag.AWGMAuth != "router-admin" {
		t.Fatalf("deploy metadata not applied: %+v", ag)
	}
	if ag.PendingVersion != "" || ag.PendingSince != "" {
		t.Fatalf("successful AWGM install must clear stale pending state: %+v", ag)
	}
}

func TestApplyAWGMDeploySuccessKeepsLocalRouterIPForDirectAWGM(t *testing.T) {
	ag := &AgentState{Nickname: "lab", DeployMode: "awgm", AWGMURL: "http://127.0.0.1:2222"}
	applyAWGMDeploySuccess(ag, &AWGMSystemInfo{RouterIP: "192.168.88.1"}, "v0.13.0-rc45", "api-key", false, time.Unix(1779693600, 0).UTC())

	if ag.Host != "192.168.88.1" {
		t.Fatalf("direct/local AWGM should preserve discovered router IP, got %q", ag.Host)
	}
	if ag.AWGMAuth != "api-key" || ag.LastDeployedVersion != "v0.13.0-rc45" {
		t.Fatalf("deploy metadata not applied: %+v", ag)
	}
}

func TestEnsureTopicAfterSuccessfulInstallCreatesMissingTopic(t *testing.T) {
	oldCreate := autoCreateForumTopicFunc
	defer func() { autoCreateForumTopicFunc = oldCreate }()
	calls := 0
	autoCreateForumTopicFunc = func(_ *State, _ *SecretStore, nick string) int {
		calls++
		if nick != "client-f-old" {
			t.Fatalf("nickname=%q", nick)
		}
		return 1931
	}
	ag := &AgentState{Nickname: "client-f-old"}

	ensureTopicAfterSuccessfulInstall(&State{}, &SecretStore{}, ag)

	if calls != 1 {
		t.Fatalf("topic create calls=%d", calls)
	}
	if ag.ThreadID != 1931 {
		t.Fatalf("ThreadID=%d", ag.ThreadID)
	}
}

func TestEnsureTopicAfterSuccessfulInstallKeepsExistingTopic(t *testing.T) {
	oldCreate := autoCreateForumTopicFunc
	defer func() { autoCreateForumTopicFunc = oldCreate }()
	autoCreateForumTopicFunc = func(*State, *SecretStore, string) int {
		t.Fatal("must not create a topic when ThreadID is already set")
		return 0
	}
	ag := &AgentState{Nickname: "testkeen", ThreadID: 406}

	ensureTopicAfterSuccessfulInstall(&State{}, &SecretStore{}, ag)

	if ag.ThreadID != 406 {
		t.Fatalf("ThreadID=%d", ag.ThreadID)
	}
}

func TestDoctorCanSkipDirectSSHForAWGMOnlyAgent(t *testing.T) {
	ag := &AgentState{Nickname: "del", DeployMode: "awgm", AWGMURL: "https://awg.delrp.example"}
	if !doctorShouldSkipDirectSSH(ag) {
		t.Fatal("AWGM-only agent without SSH coords should skip direct SSH doctor")
	}
	ag.Host = "192.168.0.1"
	if doctorShouldSkipDirectSSH(ag) {
		t.Fatal("agent with explicit SSH host should still run direct SSH doctor")
	}
}

func TestDoctorDirectSSHSkipWarningNamesAWGMPath(t *testing.T) {
	ag := &AgentState{Nickname: "client-g", DeployMode: "awgm", AWGMURL: "https://awg.client-g.example"}
	got := doctorDirectSSHSkipWarning(ag)
	for _, want := range []string{"client-g", "AWGM", "локальный SSH endpoint", "sync-vps"} {
		if !strings.Contains(got, want) {
			t.Fatalf("skip warning missing %q:\n%s", want, got)
		}
	}
}

func TestDoctorWarnsForDeployedAgentMissingDeployMetadata(t *testing.T) {
	ag := &AgentState{
		Nickname:            "client-g",
		Kind:                "mobile",
		LastDeployedVersion: "v0.13.0-rc59",
	}
	got := doctorDeployMetadataWarning(ag)
	for _, want := range []string{
		"metadata-gap",
		"client-g",
		"sync-vps",
		"re-enroll",
		"token отдельно не крути",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metadata warning missing %q:\n%s", want, got)
		}
	}
}

func TestDoctorFormatLastSeenParsesGoSQLiteTimestamp(t *testing.T) {
	raw := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05.999999999 -0700 MST")
	got := doctorFormatLastSeen(raw)
	if !strings.HasPrefix(got, "fresh") {
		t.Fatalf("doctor must parse Go timestamp stored by sqlite driver, got %q from %q", got, raw)
	}
}

func TestBuildUpdateAgentTokenHashSQLEscapesNickname(t *testing.T) {
	got := buildUpdateAgentTokenHashSQL("o'hara", "abc")
	if !strings.Contains(got, "nickname = 'o''hara'") {
		t.Fatalf("SQL did not escape nickname: %s", got)
	}
	if !strings.Contains(got, "SELECT changes();") {
		t.Fatalf("SQL must report affected rows: %s", got)
	}
}

func TestShouldOfferBackendMigrationAfterHostChange(t *testing.T) {
	oldBackend := BackendState{Host: "198.51.100.20", Domain: "wg-old.example"}
	newBackend := BackendState{Host: "198.51.100.21", Domain: "wg-old.example"}
	if !shouldOfferBackendMigration(oldBackend, newBackend, 2) {
		t.Fatal("host change with existing agents should offer migration")
	}
}

func TestShouldOfferBackendMigrationSkipsFreshInstall(t *testing.T) {
	oldBackend := BackendState{}
	newBackend := BackendState{Host: "198.51.100.21", Domain: "wg-new.example"}
	if shouldOfferBackendMigration(oldBackend, newBackend, 2) {
		t.Fatal("fresh install without previous backend should not offer migration")
	}
	if shouldOfferBackendMigration(BackendState{Host: "old", Domain: "old.example"}, newBackend, 0) {
		t.Fatal("migration without agents is useless and should not be offered")
	}
}

func TestApplyAdoptedBackendReplacesStaleBackend(t *testing.T) {
	st := &State{
		Backend: BackendState{
			Host:   "old-vps",
			Port:   2222,
			User:   "admin",
			Domain: "old.example",
		},
	}
	applyAdoptedBackend(st, BackendState{
		Host:    "198.51.100.10",
		Port:    22,
		User:    "root",
		SSHAuth: backendSSHAuthKey,
		KeyPath: `C:\Users\User\.ssh\id_ed25519`,
		Domain:  "wgmonitor.example",
	})
	if st.Backend.Host != "198.51.100.10" || st.Backend.Domain != "wgmonitor.example" {
		t.Fatalf("backend not replaced: %+v", st.Backend)
	}
	if st.Backend.Port != 22 || st.Backend.User != "root" {
		t.Fatalf("backend ssh coords not adopted: %+v", st.Backend)
	}
	if st.Backend.SSHAuth != backendSSHAuthKey || st.Backend.KeyPath == "" {
		t.Fatalf("backend ssh auth not adopted: %+v", st.Backend)
	}
}

func TestBuildMigrateUserUpsertSQLPreservesRawTokenHashAndMetadata(t *testing.T) {
	ag := AgentState{
		Nickname:            "testkeen",
		Host:                "192.168.0.1",
		Port:                222,
		User:                "root",
		Arch:                "mipsle",
		ThreadID:            123,
		Kind:                "mobile",
		Ring:                "canary",
		LastDeployedVersion: "v0.12.0-rc6",
		LastDeploy:          "2026-05-24T10:20:30Z",
		DeployMode:          "awgm",
		AWGMURL:             "https://testkeen.keenetic.pro",
		AWGMAuth:            "api-key",
		ExpectedMAC:         "aabbccddeeff",
	}
	got := buildMigrateUserUpsertSQL(ag, strings.Repeat("a", 64))
	for _, want := range []string{
		"INSERT INTO users",
		"'testkeen'",
		"'mobile'",
		"123",
		"'192.168.0.1'",
		"222",
		"'root'",
		"'mipsle'",
		"'v0.12.0-rc6'",
		"'canary'",
		"'2026-05-24T10:20:30Z'",
		"'awgm'",
		"'https://testkeen.keenetic.pro'",
		"'api-key'",
		"'aabbccddeeff'",
		"ON CONFLICT(nickname) DO UPDATE SET",
		"token_hash=excluded.token_hash",
		"expected_mac=excluded.expected_mac",
		"SELECT changes();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("migration SQL missing %q:\n%s", want, got)
		}
	}
}

func TestPlanMigrationTokenUsesCachedToken(t *testing.T) {
	store := &SecretStore{disk: map[string]string{
		"WG_AGENT_TOKEN_TESTKEEN": strings.Repeat("a", 64),
	}}
	plan, err := planMigrationToken(store, AgentState{Nickname: "testkeen"}, false, func() (string, error) {
		t.Fatal("generator should not be called when cached token exists")
		return "", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RawToken != strings.Repeat("a", 64) || plan.ReEnroll {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanMigrationTokenCanReEnrollWhenTokenIsLost(t *testing.T) {
	store := &SecretStore{disk: map[string]string{}}
	plan, err := planMigrationToken(store, AgentState{Nickname: "testkeen"}, true, func() (string, error) {
		return strings.Repeat("b", 64), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TokenEnv != "WG_AGENT_TOKEN_TESTKEEN" {
		t.Fatalf("TokenEnv = %q", plan.TokenEnv)
	}
	if plan.RawToken != strings.Repeat("b", 64) || !plan.ReEnroll {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanMigrationTokenFailsWhenTokenIsLostAndReEnrollDisabled(t *testing.T) {
	_, err := planMigrationToken(&SecretStore{disk: map[string]string{}}, AgentState{Nickname: "testkeen"}, false, func() (string, error) {
		return strings.Repeat("b", 64), nil
	})
	if err == nil || !strings.Contains(err.Error(), "WG_AGENT_TOKEN_TESTKEEN") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestRunPathDiscoveryStepUsesPreferredIfaceFastPath(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(2, "SSTP-A", true, "10.0.0.5"),
			mockIface(3, "SSTP-B", true, "10.1.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {err: errors.New("i/o timeout")},
			"ifIdx=2": {err: errors.New("i/o timeout")},
			"ifIdx=3": {localIP: "10.1.0.5", latency: 5 * time.Millisecond},
		},
	}
	rep, cleanup, iface, err := runPathDiscoveryStep("192.168.0.1", 222, "SSTP-B", f)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if iface != "SSTP-B" || rep.Chosen == nil || rep.Chosen.Iface != "SSTP-B" {
		t.Fatalf("preferred fast path should choose SSTP-B, iface=%q chosen=%+v", iface, rep.Chosen)
	}
	if len(f.addCalls) != 1 || f.addCalls[0] != 3 {
		t.Fatalf("preferred fast path should only probe ifIdx=3 before success, addCalls=%v", f.addCalls)
	}
}

func TestRunPathDiscoveryStepFallsBackWhenPreferredIfaceFails(t *testing.T) {
	f := &fakeProber{
		ifaces: []net.Interface{
			mockIface(2, "SSTP-A", true, "10.0.0.5"),
			mockIface(3, "SSTP-B", true, "10.1.0.5"),
		},
		dials: map[string]fakeDialResult{
			"default": {err: errors.New("i/o timeout")},
			"ifIdx=2": {localIP: "10.0.0.5", latency: 5 * time.Millisecond},
			"ifIdx=3": {err: errors.New("i/o timeout")},
		},
	}
	rep, cleanup, iface, err := runPathDiscoveryStep("192.168.0.1", 222, "SSTP-B", f)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if iface != "SSTP-A" || rep.Chosen == nil || rep.Chosen.Iface != "SSTP-A" {
		t.Fatalf("full discovery fallback should choose SSTP-A, iface=%q chosen=%+v", iface, rep.Chosen)
	}
	if len(f.addCalls) < 3 || f.addCalls[0] != 3 || f.addCalls[1] != 2 || f.addCalls[2] != 3 {
		t.Fatalf("fallback should probe preferred first, then full P2P list; addCalls=%v", f.addCalls)
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

func TestDiagnosisFromReport_RouteElevationHint(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:22", Candidates: []PathCandidate{
		{
			Iface: "wg-srv_legion_laptop",
			Index: 46,
			Kind:  PathP2P,
			Err:   errors.New("addRoute 192.168.0.1 via wg-srv_legion_laptop: route ADD 192.168.0.1/32 IF 46: exit status 1: The requested operation requires elevation."),
		},
	}}
	msg := diagnosisFromReport(rep, "")
	wants := []string{"AllowedIPs"}
	if runtime.GOOS == "windows" {
		wants = append(wants,
			"PowerShell от имени администратора",
			"route ADD 192.168.0.1 MASK 255.255.255.255 0.0.0.0 IF 46 METRIC 1",
		)
	} else {
		wants = append(wants,
			"sudo ip route add 192.168.0.1/32 dev wg-srv_legion_laptop metric 1",
		)
	}
	for _, want := range wants {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in diagnosis:\n%s", want, msg)
		}
	}
}

func TestRouteFixCandidateDetectsElevation(t *testing.T) {
	rep := &PathReport{Target: "192.168.0.1:22", Candidates: []PathCandidate{
		{Iface: "Ethernet", Index: 20, Kind: PathLAN, Err: errors.New("timeout")},
		{
			Iface: "wg-home",
			Index: 46,
			Kind:  PathP2P,
			Err:   errors.New("route ADD 192.168.0.1/32 IF 46: The requested operation requires elevation."),
		},
	}}
	got, ok := routeFixCandidate(rep)
	if !ok {
		t.Fatal("expected route fix candidate")
	}
	if got.Iface != "wg-home" || got.Index != 46 {
		t.Fatalf("unexpected candidate: %+v", got)
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
