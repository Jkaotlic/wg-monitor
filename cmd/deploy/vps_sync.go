package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// RemoteAgent mirrors the GET /v1/wizard/agents JSON shape.
type RemoteAgent struct {
	Nickname            string     `json:"nickname"`
	Kind                string     `json:"kind"`
	ThreadID            int64      `json:"thread_id"`
	SSHHost             string     `json:"ssh_host"`
	SSHPort             int64      `json:"ssh_port"`
	SSHUser             string     `json:"ssh_user"`
	Arch                string     `json:"arch"`
	LastDeployedVersion string     `json:"last_deployed_version"`
	Ring                string     `json:"ring"`
	PendingVersion      string     `json:"pending_version"`
	PendingSince        string     `json:"pending_since"`
	LastSeenAt          *time.Time `json:"last_seen_at,omitempty"`
	HasTopic            bool       `json:"has_topic"`
}

type wizardAgentListWire struct {
	Agents []RemoteAgent `json:"agents"`
}

type EnrollmentRequest struct {
	Nickname string `json:"nickname"`
	Kind     string `json:"kind"`
	ThreadID int64  `json:"thread_id,omitempty"`
}

type EnrollmentResponse struct {
	Nickname   string `json:"nickname"`
	BackendURL string `json:"backend_url"`
	RawToken   string `json:"raw_token"`
}

// VPSClient is a tiny HTTP client for the /v1/wizard/* endpoints.
type VPSClient struct {
	BaseURL string // e.g. "https://mon.example.com"
	Token   string
	HTTP    *http.Client
}

// NewVPSClient assembles a client from wizard state. Returns nil if the
// state is incomplete (no domain or no token) — callers should treat nil
// as "sync disabled, skip silently".
func NewVPSClient(domain, token string) *VPSClient {
	return NewVPSClientWithTimeout(domain, token, 10*time.Second)
}

func NewVPSClientWithTimeout(domain, token string, timeout time.Duration) *VPSClient {
	return NewVPSClientWithTimeoutAndDialHost(domain, token, timeout, "")
}

func NewVPSClientForBackend(state *State, token string, timeout time.Duration) *VPSClient {
	if state == nil {
		return nil
	}
	return NewVPSClientWithTimeoutAndDialHost(state.Backend.Domain, token, timeout, state.Backend.Host)
}

func NewVPSClientWithTimeoutAndDialHost(domain, token string, timeout time.Duration, dialHost string) *VPSClient {
	if domain == "" || token == "" {
		return nil
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if dialHost = strings.TrimSpace(dialHost); dialHost != "" {
		dialer := &net.Dialer{Timeout: timeout}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return dialer.DialContext(ctx, network, addr)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(dialHost, port))
		}
	}
	return &VPSClient{
		BaseURL: strings.TrimRight(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: timeout, Transport: transport},
	}
}

func (c *VPSClient) ListAgents(ctx context.Context) ([]RemoteAgent, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/v1/wizard/agents", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET /v1/wizard/agents: HTTP %d", resp.StatusCode)
	}
	var out wizardAgentListWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Agents, nil
}

func (c *VPSClient) PushAgent(ctx context.Context, a RemoteAgent) error {
	body, err := json.Marshal(struct {
		SSHHost             string `json:"ssh_host"`
		SSHPort             int64  `json:"ssh_port"`
		SSHUser             string `json:"ssh_user"`
		Arch                string `json:"arch"`
		LastDeployedVersion string `json:"last_deployed_version"`
		Ring                string `json:"ring"`
		PendingVersion      string `json:"pending_version"`
		PendingSince        string `json:"pending_since"`
	}{a.SSHHost, a.SSHPort, a.SSHUser, a.Arch, a.LastDeployedVersion, a.Ring, a.PendingVersion, a.PendingSince})
	if err != nil {
		return err
	}
	u := c.BaseURL + "/v1/wizard/agents/" + url.PathEscape(a.Nickname)
	req, err := http.NewRequestWithContext(ctx, "PUT", u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("PUT /v1/wizard/agents/%s: HTTP %d", a.Nickname, resp.StatusCode)
	}
	return nil
}

func (c *VPSClient) CreateEnrollment(ctx context.Context, enroll EnrollmentRequest) (*EnrollmentResponse, error) {
	body, err := json.Marshal(enroll)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/wizard/enrollments", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("POST /v1/wizard/enrollments: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out EnrollmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.RawToken == "" || out.BackendURL == "" {
		return nil, fmt.Errorf("backend returned incomplete enrollment")
	}
	return &out, nil
}

// MergeAgents reconciles local state.Agents with the remote view.
// Rules:
//   - remote-only entries → appended to merged with whatever SSH info remote
//     has (may be empty if VPS never received a push for them)
//   - both-present → merged inherits remote.LastDeployedVersion and remote
//     ThreadID; SSH fields come from remote IF remote has them non-empty,
//     else local SSH wins (remote NULL = "unknown", not "delete")
//   - local-only entries → kept as-is (warn separately upstream)
//   - SSH-divergence (both have non-empty SSH but differ) → divergent slice
//     surfaced for logging; merged takes remote
func MergeAgents(local []AgentState, remote []RemoteAgent) (merged []AgentState, added []string, divergent []string) {
	byNick := make(map[string]int, len(local))
	for i, a := range local {
		byNick[a.Nickname] = i
	}
	merged = append([]AgentState(nil), local...) // copy
	for _, r := range remote {
		idx, ok := byNick[r.Nickname]
		if !ok {
			merged = append(merged, AgentState{
				Nickname:            r.Nickname,
				Host:                r.SSHHost,
				Port:                int(r.SSHPort),
				User:                r.SSHUser,
				Arch:                r.Arch,
				Kind:                r.Kind,
				ThreadID:            int(r.ThreadID),
				LastDeployedVersion: r.LastDeployedVersion,
				Ring:                r.Ring,
				PendingVersion:      r.PendingVersion,
				PendingSince:        r.PendingSince,
			})
			added = append(added, r.Nickname)
			continue
		}
		a := &merged[idx]
		// remote-authoritative fields
		if r.ThreadID != 0 {
			a.ThreadID = int(r.ThreadID)
		}
		if r.Kind != "" {
			a.Kind = r.Kind
		}
		if r.LastDeployedVersion != "" {
			a.LastDeployedVersion = r.LastDeployedVersion
		}
		if r.Ring != "" {
			a.Ring = r.Ring
		}
		if r.PendingVersion != "" {
			a.PendingVersion = r.PendingVersion
		}
		if r.PendingSince != "" {
			a.PendingSince = r.PendingSince
		}
		// SSH: remote wins iff remote has value; else preserve local.
		// Track divergence (both non-empty AND differ) for visibility.
		if r.SSHHost != "" {
			if a.Host != "" && a.Host != r.SSHHost {
				divergent = append(divergent, r.Nickname)
			}
			a.Host = r.SSHHost
		}
		if r.SSHPort != 0 {
			a.Port = int(r.SSHPort)
		}
		if r.SSHUser != "" {
			a.User = r.SSHUser
		}
		if r.Arch != "" {
			a.Arch = r.Arch
		}
	}
	return
}

// Deploy enqueues a self_update command for the named agent and returns the
// backend-assigned command ID. The ID is used by AwaitCommandResult to fetch
// the agent's response once it completes. Network/HTTP errors propagate; a
// non-202 response is wrapped with the status code so the caller can show it
// to the operator. Default timeout matches the rest of VPSClient (~10s);
// callers needing more provide a longer-lived ctx.
func (c *VPSClient) Deploy(ctx context.Context, nickname, targetVersion string) (string, error) {
	if nickname == "" {
		return "", fmt.Errorf("nickname required")
	}
	if targetVersion == "" {
		return "", fmt.Errorf("target_version required")
	}
	body, err := json.Marshal(struct {
		TargetVersion string `json:"target_version"`
	}{targetVersion})
	if err != nil {
		return "", err
	}
	u := c.BaseURL + "/v1/wizard/agents/" + url.PathEscape(nickname) + "/deploy"
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("POST /v1/wizard/agents/%s/deploy: HTTP %d: %s",
			nickname, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.CmdID == "" {
		return "", fmt.Errorf("backend returned empty cmd_id")
	}
	return out.CmdID, nil
}

// AwaitCommandResult long-polls /v1/wizard/cmd/{id} until the agent posts
// a CommandResult, the per-call wait_sec elapses, or ctx is cancelled. The
// backend caps wait_sec at 60; callers needing longer waits invoke us
// repeatedly inside a deadline-bounded loop. Returns (nil, nil) on the
// 404 "result_not_ready" path so callers can distinguish "not done yet"
// from a real failure.
func (c *VPSClient) AwaitCommandResult(ctx context.Context, nickname, cmdID string, waitSec int) (*wire.CommandResult, error) {
	if cmdID == "" {
		return nil, fmt.Errorf("cmd_id required")
	}
	if waitSec <= 0 {
		waitSec = 30
	}
	if waitSec > 60 {
		waitSec = 60
	}
	u := c.BaseURL + "/v1/wizard/cmd/" + url.PathEscape(cmdID) +
		"?nickname=" + url.QueryEscape(nickname) +
		"&wait_sec=" + fmt.Sprint(waitSec)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)

	// Use a per-call client so the wait_sec long-poll isn't cut off by the
	// VPSClient.HTTP default 10s timeout.
	cli := &http.Client{Timeout: time.Duration(waitSec+5) * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// "result_not_ready" — distinguishable from real 404 by the body
		// shape, but we don't need to inspect it; the caller polls again.
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GET /v1/wizard/cmd/%s: HTTP %d: %s",
			cmdID, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out wire.CommandResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AgentStateToRemote converts a wizard-local AgentState to the RemoteAgent
// payload the PUT endpoint expects. Empty Arch falls back to amd64 as a
// last-resort default — the wizard already prompts during install so this
// rarely triggers.
func AgentStateToRemote(a AgentState) RemoteAgent {
	arch := a.Arch
	if arch == "" {
		arch = "amd64"
	}
	return RemoteAgent{
		Nickname:            a.Nickname,
		SSHHost:             a.Host,
		SSHPort:             int64(a.Port),
		SSHUser:             a.User,
		Arch:                arch,
		LastDeployedVersion: a.LastDeployedVersion,
		Ring:                a.Ring,
		PendingVersion:      a.PendingVersion,
		PendingSince:        a.PendingSince,
	}
}

// HeartbeatStatus fetches /v1/wizard/agents, finds the named agent and
// returns a human-readable freshness tag. Used by diagnoseUnreachable to
// help operator tell "router offline" from "wizard can't see router (but
// VPS can)". Empty string on any error — caller silently skips this hint.
func (c *VPSClient) HeartbeatStatus(ctx context.Context, nickname string) string {
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return ""
	}
	for _, a := range agents {
		if a.Nickname == nickname {
			return formatHeartbeatStatus(a.LastSeenAt, time.Now())
		}
	}
	return ""
}

// formatHeartbeatStatus renders a *time.Time relative to now as one of:
//
//	"fresh ~30s" / "fresh ~5m" / "stale 14m" / "stale 2h" / "never".
//
// "fresh" cutoff is 5 minutes — anything older is "stale". Nil → "never".
// Pure function, no clock dependency — caller passes "now" for testability.
func formatHeartbeatStatus(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	age := now.Sub(*t)
	const freshCutoff = 5 * time.Minute
	if age < freshCutoff {
		if age < time.Minute {
			return fmt.Sprintf("fresh ~%ds", int(age.Seconds()))
		}
		return fmt.Sprintf("fresh ~%dm", int(age.Minutes()))
	}
	if age < time.Hour {
		return fmt.Sprintf("stale %dm", int(age.Minutes()))
	}
	return fmt.Sprintf("stale %dh", int(age.Hours()))
}
