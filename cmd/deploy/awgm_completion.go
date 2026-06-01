package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type awgmAuthProbeStatus int

const (
	awgmAuthProbeSkipped awgmAuthProbeStatus = iota
	awgmAuthProbeOK
	awgmAuthProbeFail
)

type awgmInstallCompletionEvidence struct {
	Nickname       string
	Version        string
	LocalAction    string
	MinHeartbeatAt time.Time
	Remote         *RemoteAgent
	ListErr        error
	Auth           awgmAuthProbeStatus
	Now            time.Time
}

type awgmInstallCompletionReport struct {
	Confirmed bool
	Message   string
}

func reportAWGMInstallCompletion(ctx context.Context, vps *VPSClient, state *State, ag *AgentState, version, backendURL, rawToken string, minHeartbeatAt time.Time) awgmInstallCompletionReport {
	return reportAgentInstallCompletion(ctx, vps, state, ag, version, backendURL, rawToken, minHeartbeatAt, "local AWG bootstrap")
}

func reportAgentInstallCompletion(ctx context.Context, vps *VPSClient, state *State, ag *AgentState, version, backendURL, rawToken string, minHeartbeatAt time.Time, localAction string) awgmInstallCompletionReport {
	e := awgmInstallCompletionEvidence{Version: version, LocalAction: strings.TrimSpace(localAction), MinHeartbeatAt: minHeartbeatAt, Auth: awgmInstallAuthProbeStatus(state, backendURL, rawToken), Now: time.Now()}
	if ag != nil {
		e.Nickname = ag.Nickname
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if vps == nil {
		e.ListErr = fmt.Errorf("wizard API client unavailable")
		return awgmInstallCompletionSummary(e)
	}
	agents, err := vps.ListAgents(ctx)
	if err != nil {
		e.ListErr = err
		return awgmInstallCompletionSummary(e)
	}
	for i := range agents {
		if agents[i].Nickname == e.Nickname {
			e.Remote = &agents[i]
			break
		}
	}
	return awgmInstallCompletionSummary(e)
}

func awgmInstallAuthProbeStatus(state *State, backendURL, rawToken string) awgmAuthProbeStatus {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return awgmAuthProbeSkipped
	}
	url := awgmInstallAuthProbeURL(state, backendURL)
	if url == "" {
		return awgmAuthProbeSkipped
	}
	if probeAgentTokenValid(url, rawToken) {
		return awgmAuthProbeOK
	}
	return awgmAuthProbeFail
}

func awgmInstallAuthProbeURL(state *State, backendURL string) string {
	base := strings.TrimSpace(backendURL)
	if base == "" && state != nil {
		base = strings.TrimSpace(state.Backend.Domain)
	}
	if base == "" {
		return ""
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return strings.TrimRight(base, "/") + "/v1/cmd?wait=0"
}

func awgmInstallCompletionSummary(e awgmInstallCompletionEvidence) awgmInstallCompletionReport {
	if e.Now.IsZero() {
		e.Now = time.Now()
	}
	localAction := strings.TrimSpace(e.LocalAction)
	if localAction == "" {
		localAction = "local install"
	}
	heartbeat := "heartbeat unknown"
	version := strings.TrimSpace(e.Version)
	remoteVersion := ""
	remoteFound := e.Remote != nil
	if e.Remote != nil {
		heartbeat = "heartbeat " + formatHeartbeatStatus(e.Remote.LastSeenAt, e.Now)
		remoteVersion = strings.TrimSpace(e.Remote.LastDeployedVersion)
	}
	auth := awgmAuthProbeText(e.Auth)
	confirmed := e.ListErr == nil &&
		remoteFound &&
		version != "" &&
		remoteVersion == version &&
		awgmHeartbeatFresh(e.Remote.LastSeenAt, e.Now) &&
		awgmHeartbeatAfterInstall(e.Remote.LastSeenAt, e.MinHeartbeatAt) &&
		e.Auth == awgmAuthProbeOK

	if confirmed {
		return awgmInstallCompletionReport{
			Confirmed: true,
			Message:   fmt.Sprintf("backend-confirmed: %s %s; %s; %s", e.Nickname, version, heartbeat, auth),
		}
	}

	var reasons []string
	if e.ListErr != nil {
		reasons = append(reasons, "wizard list failed: "+e.ListErr.Error())
	} else if !remoteFound {
		reasons = append(reasons, "backend agent not found")
	} else {
		if remoteVersion != version {
			reasons = append(reasons, fmt.Sprintf("backend version %q != %q", remoteVersion, version))
		}
		if !awgmHeartbeatFresh(e.Remote.LastSeenAt, e.Now) {
			reasons = append(reasons, heartbeat)
		}
		if awgmHeartbeatFresh(e.Remote.LastSeenAt, e.Now) && !awgmHeartbeatAfterInstall(e.Remote.LastSeenAt, e.MinHeartbeatAt) {
			reasons = append(reasons, "heartbeat before install")
		}
	}
	if e.Auth != awgmAuthProbeOK {
		reasons = append(reasons, auth)
	}
	return awgmInstallCompletionReport{
		Message: fmt.Sprintf(
			"pending: %s finished for %s %s, but backend confirmation is incomplete (%s). Run doctor; if auth-probe stays failed, check token_hash.",
			localAction,
			e.Nickname,
			version,
			strings.Join(reasons, "; "),
		),
	}
}

func awgmAuthProbeText(s awgmAuthProbeStatus) string {
	switch s {
	case awgmAuthProbeOK:
		return "auth-probe valid"
	case awgmAuthProbeFail:
		return "auth-probe failed"
	default:
		return "auth-probe skipped"
	}
}

func awgmHeartbeatFresh(t *time.Time, now time.Time) bool {
	if t == nil {
		return false
	}
	age := now.Sub(*t)
	if age < -2*time.Minute {
		return false
	}
	if age < 0 {
		age = 0
	}
	return age < 5*time.Minute
}

func awgmHeartbeatAfterInstall(t *time.Time, minHeartbeatAt time.Time) bool {
	if t == nil || minHeartbeatAt.IsZero() {
		return t != nil
	}
	return !t.Before(minHeartbeatAt.Add(-2 * time.Second))
}
