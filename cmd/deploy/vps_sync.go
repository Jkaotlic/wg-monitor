package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RemoteAgent mirrors the GET /v1/wizard/agents JSON shape.
type RemoteAgent struct {
	Nickname            string `json:"nickname"`
	Kind                string `json:"kind"`
	ThreadID            int64  `json:"thread_id"`
	SSHHost             string `json:"ssh_host"`
	SSHPort             int64  `json:"ssh_port"`
	SSHUser             string `json:"ssh_user"`
	Arch                string `json:"arch"`
	LastDeployedVersion string `json:"last_deployed_version"`
	HasTopic            bool   `json:"has_topic"`
}

type wizardAgentListWire struct {
	Agents []RemoteAgent `json:"agents"`
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
	if domain == "" || token == "" {
		return nil
	}
	base := domain
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return &VPSClient{
		BaseURL: strings.TrimRight(base, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
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
	}{a.SSHHost, a.SSHPort, a.SSHUser, a.Arch, a.LastDeployedVersion})
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
	}
}
