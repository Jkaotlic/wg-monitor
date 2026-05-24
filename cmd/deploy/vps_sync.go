package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	LastDeploy          string     `json:"last_deploy"`
	DeployMode          string     `json:"deploy_mode"`
	AWGMURL             string     `json:"awgm_url"`
	AWGMAuth            string     `json:"awgm_auth"`
	ExpectedMAC         string     `json:"expected_mac"`
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

	Fallback wizardAPIFallback
}

type wizardAPIFallback interface {
	DoWizardAPI(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error)
}

type wizardAPIFallbackFunc func(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error)

func (f wizardAPIFallbackFunc) DoWizardAPI(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	return f(ctx, method, path, body, timeout)
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

func NewResilientVPSClientForBackend(state *State, secrets *SecretStore, token string, timeout time.Duration) *VPSClient {
	c := NewVPSClientForBackend(state, token, timeout)
	if c == nil || state == nil || secrets == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return c
	}
	c.Fallback = &wizardAPISSHFallback{state: state, secrets: secrets, token: token}
	return c
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
	status, raw, err := c.doWizardAPI(ctx, http.MethodGet, "/v1/wizard/agents", nil, "", 0)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/wizard/agents: HTTP %d", status)
	}
	var out wizardAgentListWire
	if err := json.Unmarshal(raw, &out); err != nil {
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
		LastDeploy          string `json:"last_deploy"`
		DeployMode          string `json:"deploy_mode"`
		AWGMURL             string `json:"awgm_url"`
		AWGMAuth            string `json:"awgm_auth"`
		ExpectedMAC         string `json:"expected_mac"`
	}{
		a.SSHHost, a.SSHPort, a.SSHUser, a.Arch, a.LastDeployedVersion, a.Ring, a.PendingVersion, a.PendingSince,
		a.LastDeploy, a.DeployMode, a.AWGMURL, a.AWGMAuth, a.ExpectedMAC,
	})
	if err != nil {
		return err
	}
	path := "/v1/wizard/agents/" + url.PathEscape(a.Nickname)
	status, raw, err := c.doWizardAPI(ctx, http.MethodPut, path, body, "application/json", 0)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return fmt.Errorf("PUT /v1/wizard/agents/%s: HTTP %d: %s", a.Nickname, status, trimWizardBody(raw))
	}
	return nil
}

func (c *VPSClient) CreateEnrollment(ctx context.Context, enroll EnrollmentRequest) (*EnrollmentResponse, error) {
	body, err := json.Marshal(enroll)
	if err != nil {
		return nil, err
	}
	status, raw, err := c.doWizardAPI(ctx, http.MethodPost, "/v1/wizard/enrollments", body, "application/json", 0)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, fmt.Errorf("POST /v1/wizard/enrollments: HTTP %d: %s",
			status, trimWizardBody(raw))
	}
	var out EnrollmentResponse
	if err := json.Unmarshal(raw, &out); err != nil {
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
func MergeAgents(local []AgentState, remote []RemoteAgent) (merged []AgentState, added []string, divergent []string, macConflicts []string) {
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
				LastDeploy:          r.LastDeploy,
				DeployMode:          r.DeployMode,
				AWGMURL:             normalizeAWGMURL(r.AWGMURL),
				AWGMAuth:            r.AWGMAuth,
				ExpectedMAC:         normalizeMACPin(r.ExpectedMAC),
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
		a.PendingVersion = r.PendingVersion
		a.PendingSince = r.PendingSince
		if r.LastDeploy != "" {
			a.LastDeploy = r.LastDeploy
		}
		if r.DeployMode != "" {
			a.DeployMode = r.DeployMode
		}
		if r.AWGMURL != "" {
			a.AWGMURL = normalizeAWGMURL(r.AWGMURL)
		}
		if r.AWGMAuth != "" {
			a.AWGMAuth = r.AWGMAuth
		}
		if r.ExpectedMAC != "" {
			remoteMAC := normalizeMACPin(r.ExpectedMAC)
			localMAC := normalizeMACPin(a.ExpectedMAC)
			if localMAC == "" {
				a.ExpectedMAC = remoteMAC
			} else if remoteMAC != "" && localMAC != remoteMAC {
				a.ExpectedMAC = localMAC
				macConflicts = append(macConflicts, r.Nickname)
			}
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

func normalizeMACPin(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
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
	path := "/v1/wizard/agents/" + url.PathEscape(nickname) + "/deploy"
	status, raw, err := c.doWizardAPI(ctx, http.MethodPost, path, body, "application/json", 0)
	if err != nil {
		return "", err
	}
	if status != http.StatusAccepted {
		return "", fmt.Errorf("POST /v1/wizard/agents/%s/deploy: HTTP %d: %s",
			nickname, status, trimWizardBody(raw))
	}
	var out struct {
		CmdID string `json:"cmd_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
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
	path := "/v1/wizard/cmd/" + url.PathEscape(cmdID) +
		"?nickname=" + url.QueryEscape(nickname) +
		"&wait_sec=" + fmt.Sprint(waitSec)
	status, raw, err := c.doWizardAPI(ctx, http.MethodGet, path, nil, "", time.Duration(waitSec+5)*time.Second)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		// "result_not_ready" — distinguishable from real 404 by the body
		// shape, but we don't need to inspect it; the caller polls again.
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/wizard/cmd/%s: HTTP %d: %s",
			cmdID, status, trimWizardBody(raw))
	}
	var out wire.CommandResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *VPSClient) doWizardAPI(ctx context.Context, method, path string, body []byte, contentType string, timeout time.Duration) (int, []byte, error) {
	status, raw, err := c.doWizardHTTP(ctx, method, path, body, contentType, timeout)
	if err == nil {
		return status, raw, nil
	}
	if ctx.Err() != nil {
		return 0, nil, err
	}
	if c.Fallback == nil || !isWizardTransportErr(err) {
		return 0, nil, err
	}
	PrintWarn("wizard API по HTTPS не отвечает, пробую запасной путь через SSH на VPS")
	return c.Fallback.DoWizardAPI(ctx, method, path, body, timeout)
}

func (c *VPSClient) doWizardHTTP(ctx context.Context, method, path string, body []byte, contentType string, timeout time.Duration) (int, []byte, error) {
	if c == nil {
		return 0, nil, fmt.Errorf("VPS client is nil")
	}
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClientFor(timeout).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func (c *VPSClient) httpClientFor(timeout time.Duration) *http.Client {
	if c.HTTP == nil {
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		return &http.Client{Timeout: timeout}
	}
	if timeout <= 0 || timeout == c.HTTP.Timeout {
		return c.HTTP
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     c.HTTP.Transport,
		CheckRedirect: c.HTTP.CheckRedirect,
		Jar:           c.HTTP.Jar,
	}
}

func isWizardTransportErr(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"timeout",
		"deadline exceeded",
		"connection refused",
		"connection reset",
		"connection aborted",
		"no such host",
		"server misbehaving",
		"tls handshake timeout",
		"network is unreachable",
		"no route to host",
		"unexpected eof",
		"eof",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

type wizardAPISSHFallback struct {
	state   *State
	secrets *SecretStore
	token   string
}

func (f *wizardAPISSHFallback) DoWizardAPI(ctx context.Context, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	if f == nil || f.state == nil || f.secrets == nil {
		return 0, nil, fmt.Errorf("SSH fallback is not configured")
	}
	if ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return 0, nil, err
	}
	auth, err := backendSSHAuthMethodsNonInteractive(f.state, f.secrets)
	if err != nil {
		return 0, nil, err
	}
	port := portOrDefault(f.state.Backend.Port, 22)
	user := userOrDefault(f.state.Backend.User, "root")
	var s *SSH
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		s, err = ConnectSSHWithAuth(f.state.Backend.Host, port, user, auth, kh, "backend")
		if err == nil || !isTransientSSHTimeout(err) || attempt == 3 {
			break
		}
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("SSH fallback to VPS: %w", err)
	}
	defer s.Close()

	cmd := buildWizardAPICurlCommand(method, path, f.token, body, timeout)
	out, serr, rc, err := s.Run(cmd)
	if err != nil {
		return 0, nil, fmt.Errorf("SSH fallback transport: %w", err)
	}
	if rc != 0 {
		msg := strings.TrimSpace(serr)
		if msg == "" {
			msg = trimWizardBody([]byte(out))
		}
		return 0, nil, fmt.Errorf("SSH fallback curl failed rc=%d: %s", rc, msg)
	}
	status, raw, err := parseWizardAPISSHOutput(out)
	if err != nil {
		return 0, nil, err
	}
	return status, raw, nil
}

const wizardAPIStatusPrefix = "__WG_WIZARD_HTTP_STATUS__"

func buildWizardAPICurlCommand(method, path, token string, body []byte, timeout time.Duration) string {
	secs := int((timeout + time.Second - 1) / time.Second)
	if secs < 5 {
		secs = 5
	}
	if secs > 90 {
		secs = 90
	}
	url := "http://127.0.0.1:8080" + path
	common := "curl -sS --max-time " + strconv.Itoa(secs) +
		" -o \"$tmp\" -w '%{http_code}'" +
		" -X " + shellSingleQuote(method) +
		" -H " + shellSingleQuote("Authorization: Bearer "+token)
	if body != nil {
		common += " -H " + shellSingleQuote("Content-Type: application/json")
	}
	common += " " + shellSingleQuote(url)

	cmd := "tmp=$(mktemp /tmp/wg-wizard-api.XXXXXX) || exit 90; trap 'rm -f \"$tmp\"' EXIT; "
	if body != nil {
		cmd += "code=$(printf %s " + shellSingleQuote(base64.StdEncoding.EncodeToString(body)) +
			" | base64 -d | " + common + " --data-binary @-); "
	} else {
		cmd += "code=$(" + common + "); "
	}
	cmd += "rc=$?; printf '%s%s\\n' " + shellSingleQuote(wizardAPIStatusPrefix) + " \"$code\"; cat \"$tmp\"; exit \"$rc\""
	return cmd
}

func parseWizardAPISSHOutput(out string) (int, []byte, error) {
	line, rest, ok := strings.Cut(out, "\n")
	if !ok {
		return 0, nil, fmt.Errorf("SSH fallback returned malformed response")
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, wizardAPIStatusPrefix) {
		return 0, nil, fmt.Errorf("SSH fallback returned malformed status")
	}
	statusRaw := strings.TrimPrefix(line, wizardAPIStatusPrefix)
	status, err := strconv.Atoi(statusRaw)
	if err != nil || status < 100 || status > 599 {
		return 0, nil, fmt.Errorf("SSH fallback returned invalid HTTP status %q", statusRaw)
	}
	return status, []byte(rest), nil
}

func trimWizardBody(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 1024 {
		return s[:1024] + "..."
	}
	return s
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
		LastDeploy:          a.LastDeploy,
		DeployMode:          a.DeployMode,
		AWGMURL:             normalizeAWGMURL(a.AWGMURL),
		AWGMAuth:            a.AWGMAuth,
		ExpectedMAC:         normalizeMACPin(a.ExpectedMAC),
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
	const futureSkewCutoff = 2 * time.Minute
	if age < -futureSkewCutoff {
		return fmt.Sprintf("clock-skew future %dm", int((-age).Minutes()))
	}
	if age < 0 {
		age = 0
	}
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
