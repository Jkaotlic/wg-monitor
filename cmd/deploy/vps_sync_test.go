package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMergeAgents_EmptyLocal_AllAdded(t *testing.T) {
	local := []AgentState{}
	remote := []RemoteAgent{{Nickname: "client-a", SSHHost: "10.0.0.1", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	merged, added, _, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "client-a" || merged[0].Host != "10.0.0.1" {
		t.Fatalf("merged: %+v", merged)
	}
	if !reflect.DeepEqual(added, []string{"client-a"}) {
		t.Fatalf("added: %+v", added)
	}
}

func TestMergeAgents_RemoteOverridesLocal(t *testing.T) {
	local := []AgentState{{Nickname: "client-a", Host: "old", Port: 22, User: "root", Arch: "mips", LastDeployedVersion: "v0.9"}}
	remote := []RemoteAgent{{Nickname: "client-a", SSHHost: "new", SSHPort: 222, SSHUser: "root", Arch: "mips", LastDeployedVersion: "v0.10.3", Ring: "canary", PendingVersion: "v0.11.0", PendingSince: "2026-05-19T10:00:00Z"}}
	merged, added, divergent, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Host != "new" || merged[0].Port != 222 || merged[0].LastDeployedVersion != "v0.10.3" {
		t.Fatalf("merged: %+v", merged)
	}
	if merged[0].Ring != "canary" || merged[0].PendingVersion != "v0.11.0" {
		t.Fatalf("rollout metadata not merged: %+v", merged[0])
	}
	if len(added) != 0 {
		t.Fatalf("want 0 added, got %v", added)
	}
	if len(divergent) != 1 || divergent[0] != "client-a" {
		t.Fatalf("want 1 divergent, got %v", divergent)
	}
}

func TestMergeAgents_RemoteClearsPendingState(t *testing.T) {
	local := []AgentState{{
		Nickname:            "client-h",
		LastDeployedVersion: "v0.13.0",
		PendingVersion:      "v0.14.0-rc1",
		PendingSince:        "2026-05-19T10:00:00Z",
	}}
	remote := []RemoteAgent{{
		Nickname:            "client-h",
		LastDeployedVersion: "v0.14.0-rc1",
	}}

	merged, _, _, _ := MergeAgents(local, remote)
	if merged[0].PendingVersion != "" || merged[0].PendingSince != "" {
		t.Fatalf("remote empty pending must clear stale local pending: %+v", merged[0])
	}
}

func TestMergeAgents_RemoteNullPreservesLocalSSH(t *testing.T) {
	// Remote has no SSH (NULLs from DB), local has it. Local wins for SSH
	// because remote NULLs are "unknown" not "delete".
	local := []AgentState{{Nickname: "client-a", Host: "192.168.1.1", Port: 222, User: "root", Arch: "mips", LastDeployedVersion: "v0.10.3"}}
	remote := []RemoteAgent{{Nickname: "client-a"}} // all empty
	merged, _, _, _ := MergeAgents(local, remote)
	if merged[0].Host != "192.168.1.1" || merged[0].Port != 222 {
		t.Fatalf("local SSH lost: %+v", merged[0])
	}
}

func TestMergeAgents_LocalOnlyKept(t *testing.T) {
	local := []AgentState{{Nickname: "ghost", Host: "1.1.1.1", Port: 22, User: "root", Arch: "mips"}}
	remote := []RemoteAgent{}
	merged, _, _, _ := MergeAgents(local, remote)
	if len(merged) != 1 || merged[0].Nickname != "ghost" {
		t.Fatalf("local-only dropped: %+v", merged)
	}
}

func TestMergeAgents_PullsPortableAWGMMetadata(t *testing.T) {
	local := []AgentState{{
		Nickname:       "client-a",
		PreferredIface: "SSTP client-a",
	}}
	remote := []RemoteAgent{{
		Nickname:    "client-a",
		LastDeploy:  "2026-05-24T10:20:30Z",
		DeployMode:  "awgm",
		AWGMURL:     "https://client-a.keenetic.example",
		AWGMAuth:    "api-key",
		ExpectedMAC: "aabbccddeeff",
	}}

	merged, _, _, macConflicts := MergeAgents(local, remote)
	got := merged[0]
	if got.LastDeploy != "2026-05-24T10:20:30Z" || got.DeployMode != "awgm" ||
		got.AWGMURL != "https://client-a.keenetic.example" || got.AWGMAuth != "api-key" ||
		got.ExpectedMAC != "aabbccddeeff" {
		t.Fatalf("portable metadata not merged: %+v", got)
	}
	if got.PreferredIface != "SSTP client-a" {
		t.Fatalf("machine-local preferred_iface must be preserved locally: %+v", got)
	}
	if len(macConflicts) != 0 {
		t.Fatalf("unexpected mac conflicts: %v", macConflicts)
	}
}

func TestMergeAgents_PreservesLocalExpectedMACOnRemoteConflict(t *testing.T) {
	local := []AgentState{{Nickname: "client-a", ExpectedMAC: "111111111111"}}
	remote := []RemoteAgent{{Nickname: "client-a", ExpectedMAC: "222222222222"}}

	merged, _, _, macConflicts := MergeAgents(local, remote)
	if merged[0].ExpectedMAC != "111111111111" {
		t.Fatalf("local expected_mac overwritten: %+v", merged[0])
	}
	if !reflect.DeepEqual(macConflicts, []string{"client-a"}) {
		t.Fatalf("mac conflicts=%v, want client-a", macConflicts)
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

func TestVPSClientCreateEnrollment(t *testing.T) {
	var sawAuth, sawJSON bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wizard/enrollments" {
			http.NotFound(w, r)
			return
		}
		sawAuth = r.Header.Get("Authorization") == "Bearer wizard-token"
		sawJSON = strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
		var req EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Nickname != "testkeen" || req.Kind != "static" || req.ThreadID != 406 {
			t.Fatalf("bad request: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"nickname":"testkeen","backend_url":"https://wg.example.test","raw_token":"raw-token"}`))
	}))
	defer srv.Close()

	c := &VPSClient{BaseURL: srv.URL, Token: "wizard-token", HTTP: srv.Client()}
	got, err := c.CreateEnrollment(t.Context(), EnrollmentRequest{
		Nickname: "testkeen",
		Kind:     "static",
		ThreadID: 406,
	})
	if err != nil {
		t.Fatalf("CreateEnrollment: %v", err)
	}
	if got.Nickname != "testkeen" || got.BackendURL != "https://wg.example.test" || got.RawToken != "raw-token" {
		t.Fatalf("bad response: %+v", got)
	}
	if !sawAuth || !sawJSON {
		t.Fatalf("headers missing: auth=%v json=%v", sawAuth, sawJSON)
	}
}

func TestVPSClientCreateEnrollmentFallsBackToSSHOnTransportError(t *testing.T) {
	var fallbackCalled bool
	c := &VPSClient{
		BaseURL: "https://wg.example.test",
		Token:   "wizard-token",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
		Fallback: wizardAPIFallbackFunc(func(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
			fallbackCalled = true
			if method != http.MethodPost {
				t.Fatalf("method=%s, want POST", method)
			}
			if path != "/v1/wizard/enrollments" {
				t.Fatalf("path=%s, want /v1/wizard/enrollments", path)
			}
			var req EnrollmentRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode fallback body: %v", err)
			}
			if req.Nickname != "client-e" || req.Kind != "mobile" || req.ThreadID != 1126 {
				t.Fatalf("bad fallback request: %+v", req)
			}
			return http.StatusCreated, []byte(`{"nickname":"client-e","backend_url":"https://wg.example.test","raw_token":"raw-token"}`), nil
		}),
	}

	got, err := c.CreateEnrollment(t.Context(), EnrollmentRequest{
		Nickname: "client-e",
		Kind:     "mobile",
		ThreadID: 1126,
	})
	if err != nil {
		t.Fatalf("CreateEnrollment: %v", err)
	}
	if !fallbackCalled {
		t.Fatal("fallback was not called")
	}
	if got.RawToken != "raw-token" {
		t.Fatalf("raw token=%q", got.RawToken)
	}
}

func TestVPSClientPushAgentCarriesKindAndThread(t *testing.T) {
	var req RemoteAgent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wizard/agents/client-g" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("method=%s, want PUT", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &VPSClient{BaseURL: srv.URL, Token: "wizard-token", HTTP: srv.Client()}
	err := c.PushAgent(t.Context(), RemoteAgent{
		Nickname: "client-g",
		Kind:     "mobile",
		ThreadID: 406,
		Arch:     "mipsle",
	})
	if err != nil {
		t.Fatalf("PushAgent: %v", err)
	}
	if req.Kind != "mobile" || req.ThreadID != 406 {
		t.Fatalf("PushAgent lost kind/thread metadata: %+v", req)
	}
}

func TestVPSClientDoesNotFallbackOnHTTPErrorStatus(t *testing.T) {
	var fallbackCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer srv.Close()

	c := &VPSClient{
		BaseURL: srv.URL,
		Token:   "wrong-token",
		HTTP:    srv.Client(),
		Fallback: wizardAPIFallbackFunc(func(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
			fallbackCalled = true
			return http.StatusOK, []byte(`{"agents":[]}`), nil
		}),
	}

	_, err := c.ListAgents(t.Context())
	if err == nil {
		t.Fatal("ListAgents succeeded unexpectedly")
	}
	if fallbackCalled {
		t.Fatal("fallback must not run for backend HTTP errors")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error=%v, want HTTP 401", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewVPSClientWithTimeoutAllowsSlowStartupSync(t *testing.T) {
	c := NewVPSClientWithTimeout("wgmonitor.example", "secret", 25*time.Second)
	if c == nil {
		t.Fatal("client is nil")
	}
	if c.HTTP.Timeout != 25*time.Second {
		t.Fatalf("timeout=%s, want 25s", c.HTTP.Timeout)
	}
	if c.BaseURL != "https://wgmonitor.example" {
		t.Fatalf("base url=%q", c.BaseURL)
	}
}

func TestNewVPSClientWithTimeoutKeepsDefaultForInvalidTimeout(t *testing.T) {
	c := NewVPSClientWithTimeout("https://wgmonitor.example/", "secret", 0)
	if c == nil {
		t.Fatal("client is nil")
	}
	if c.HTTP.Timeout != 10*time.Second {
		t.Fatalf("timeout=%s, want default 10s", c.HTTP.Timeout)
	}
	if c.BaseURL != "https://wgmonitor.example" {
		t.Fatalf("base url=%q", c.BaseURL)
	}
}

func TestNewVPSClientWithTimeoutAndDialHostKeepsDomainBaseURL(t *testing.T) {
	c := NewVPSClientWithTimeoutAndDialHost("wgmonitor.example", "secret", 22*time.Second, "198.51.100.10")
	if c == nil {
		t.Fatal("client is nil")
	}
	if c.BaseURL != "https://wgmonitor.example" {
		t.Fatalf("base url=%q", c.BaseURL)
	}
	if c.HTTP.Transport == nil {
		t.Fatal("expected custom transport")
	}
}

func TestNewVPSClientForBackendUsesDomainAndHost(t *testing.T) {
	state := &State{Backend: BackendState{Domain: "wgmonitor.example", Host: "198.51.100.10"}}
	c := NewVPSClientForBackend(state, "secret", 15*time.Second)
	if c == nil {
		t.Fatal("client is nil")
	}
	if c.BaseURL != "https://wgmonitor.example" {
		t.Fatalf("base url=%q", c.BaseURL)
	}
	if c.HTTP.Timeout != 15*time.Second {
		t.Fatalf("timeout=%s, want 15s", c.HTTP.Timeout)
	}
	if c.HTTP.Transport == nil {
		t.Fatal("expected custom transport")
	}
}

func TestNewVPSClientForBackendDoesNotPinAPIToSourceBoundSSHHost(t *testing.T) {
	state := &State{Backend: BackendState{
		Domain:     "wgmonitor.example",
		Host:       "192.168.0.87",
		SourceBind: "172.16.6.9",
	}}
	c := NewVPSClientForBackend(state, "secret", 15*time.Second)
	if c == nil {
		t.Fatal("client is nil")
	}
	if got := backendAPIDialHost(state); got != "" {
		t.Fatalf("backendAPIDialHost=%q, want empty", got)
	}
}

func TestBuildWizardAPICurlCommandSetsForwardedPublicHost(t *testing.T) {
	cmd := buildWizardAPICurlCommand(
		http.MethodPost,
		"/v1/wizard/agents/testkeen/deploy",
		"tok",
		[]byte(`{"target_version":"v0.13.0-rc79"}`),
		5*time.Second,
		"https://wgmonitor.example/path",
	)
	if !strings.Contains(cmd, "X-Forwarded-Proto: https") {
		t.Fatalf("missing forwarded proto header: %s", cmd)
	}
	if !strings.Contains(cmd, "X-Forwarded-Host: wgmonitor.example") {
		t.Fatalf("missing forwarded host header: %s", cmd)
	}
}
