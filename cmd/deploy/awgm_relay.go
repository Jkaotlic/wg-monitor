package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmrelay"
)

type awgmRelayConfig struct {
	BaseURL          string `json:"base_url"`
	APIKey           string `json:"api_key"`
	Login            string `json:"login"`
	Password         string `json:"password"`
	InsecureTLS      bool   `json:"insecure_tls,omitempty"`
	TerminalUser     string `json:"terminal_user"`
	TerminalPassword string `json:"terminal_password"`
	BootstrapScript  string `json:"bootstrap_script"`
	Mode             string `json:"mode"`
	Nickname         string `json:"nickname,omitempty"`
	Kind             string `json:"kind,omitempty"`
	ThreadID         int64  `json:"thread_id,omitempty"`
	BackendURL       string `json:"backend_url,omitempty"`
	WizardToken      string `json:"wizard_token,omitempty"`
	TargetVersion    string `json:"target_version,omitempty"`
	ReleaseBase      string `json:"release_base,omitempty"`
	ExpectedSHA      string `json:"expected_sha,omitempty"`
	InitScript       string `json:"init_script,omitempty"`
	ExpiresAtUnix    int64  `json:"expires_at_unix,omitempty"`
	RawToken         string `json:"raw_token,omitempty"`
	RecoveryHint     string `json:"recovery_hint,omitempty"`
}

func runAWGMBootstrapDirect(awgm *AWGMClient, script, terminalUser, terminalPass string) (TerminalRunResult, error) {
	st, err := awgm.TerminalStatus(context.Background())
	if err != nil {
		return TerminalRunResult{}, err
	}
	if st.SessionActive {
		return TerminalRunResult{}, fmt.Errorf("AWG Manager terminal already has an active session; close it in the web UI and retry")
	}
	if !st.Installed {
		if err := awgm.TerminalInstall(context.Background()); err != nil {
			return TerminalRunResult{}, err
		}
	}
	if !st.Running {
		if err := awgm.TerminalStart(context.Background()); err != nil {
			return TerminalRunResult{}, err
		}
	}
	defer func() {
		if err := awgm.TerminalStop(context.Background()); err != nil {
			PrintWarn("AWG Manager terminal stop: " + err.Error())
		}
	}()
	return awgm.RunTerminalScriptWithLogin(context.Background(), script, terminalUser, terminalPass)
}

func runAWGMBootstrapViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass, terminalUser, terminalPass, script string) (TerminalRunResult, error) {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return TerminalRunResult{}, fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return TerminalRunResult{}, err
	}
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return TerminalRunResult{}, err
	}
	defer bs.Close()

	nick := safeRelayName(ag.Nickname)
	stamp := time.Now().UTC().Format("20060102150405")
	base := "/tmp/wg-monitor-awgm-relay-" + nick + "-" + stamp
	scriptPath := base + ".py"
	configPath := base + ".json"
	cleanup := "rm -f " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	defer func() {
		if _, _, rc, err := bs.Run(cleanup); err != nil || rc != 0 {
			PrintWarn("AWG Manager VPS relay cleanup failed")
		}
	}()

	cfg := awgmRelayConfig{
		BaseURL:          ag.AWGMURL,
		APIKey:           apiKey,
		Login:            login,
		Password:         pass,
		InsecureTLS:      awgmInsecureTLS(),
		TerminalUser:     terminalUser,
		TerminalPassword: terminalPass,
		BootstrapScript:  script,
		Mode:             "bootstrap",
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return TerminalRunResult{}, err
	}
	if err := bs.UploadStdin(scriptPath, []byte(awgmVPSRelayPython)); err != nil {
		return TerminalRunResult{}, fmt.Errorf("upload awgm relay script: %w", err)
	}
	if err := bs.UploadStdin(configPath, cfgBytes); err != nil {
		return TerminalRunResult{}, fmt.Errorf("upload awgm relay config: %w", err)
	}
	cmd := "chmod 700 " + shellSingleQuote(scriptPath) + " && chmod 600 " + shellSingleQuote(configPath) +
		" && python3 " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return TerminalRunResult{Output: out + stderr}, fmt.Errorf("awgm vps relay ssh transport: %w", err)
	}
	if rc != 0 {
		return TerminalRunResult{Output: out + stderr}, fmt.Errorf("awgm vps relay failed rc=%d", rc)
	}
	return TerminalRunResult{Output: out}, nil
}

var runAWGMBootstrapDirectFunc = runAWGMBootstrapDirect
var runAWGMBootstrapViaVPSFunc = runAWGMBootstrapViaVPS

func fetchAWGMSystemInfoViaVPS(state *State, secrets *SecretStore, ag *AgentState, apiKey, login, pass string) (*AWGMSystemInfo, error) {
	if state == nil || strings.TrimSpace(state.Backend.Host) == "" {
		return nil, fmt.Errorf("backend SSH host is not configured; install backend first")
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return nil, err
	}
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return nil, err
	}
	defer bs.Close()

	nick := safeRelayName(ag.Nickname)
	stamp := time.Now().UTC().Format("20060102150405")
	base := "/tmp/wg-monitor-awgm-info-" + nick + "-" + stamp
	scriptPath := base + ".py"
	configPath := base + ".json"
	cleanup := "rm -f " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	defer func() {
		if _, _, rc, err := bs.Run(cleanup); err != nil || rc != 0 {
			PrintWarn("AWG Manager VPS info cleanup failed")
		}
	}()

	cfg := awgmRelayConfig{
		BaseURL:     ag.AWGMURL,
		APIKey:      apiKey,
		Login:       login,
		Password:    pass,
		InsecureTLS: awgmInsecureTLS(),
		Mode:        "system_info",
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := bs.UploadStdin(scriptPath, []byte(awgmVPSRelayPython)); err != nil {
		return nil, fmt.Errorf("upload awgm relay script: %w", err)
	}
	if err := bs.UploadStdin(configPath, cfgBytes); err != nil {
		return nil, fmt.Errorf("upload awgm relay config: %w", err)
	}
	cmd := "chmod 700 " + shellSingleQuote(scriptPath) + " && chmod 600 " + shellSingleQuote(configPath) +
		" && python3 " + shellSingleQuote(scriptPath) + " " + shellSingleQuote(configPath)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return nil, fmt.Errorf("awgm vps relay ssh transport: %w", err)
	}
	if rc != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return nil, fmt.Errorf("awgm vps system info failed rc=%d: %s", rc, msg)
	}
	return parseAWGMSystemInfoRelayOutput(out)
}

type awgmSystemInfoProbe func() (*AWGMSystemInfo, error)

type awgmBootstrapRun func() (TerminalRunResult, error)

func runAWGMBootstrapWithDirectFallback(viaVPS bool, vpsRun, directRun awgmBootstrapRun) (TerminalRunResult, bool, error) {
	if !viaVPS {
		res, err := directRun()
		return res, false, err
	}
	res, err := vpsRun()
	if err == nil {
		return res, true, nil
	}
	if !shouldTryDirectAWGMBootstrapFallback(err) {
		return res, true, err
	}
	PrintWarn("AWG Manager VPS relay terminal bootstrap failed at the web edge (" + shortAWGMErrorForOperator(err) + "); trying direct from this operator PC before defer/cancel")
	directRes, directErr := directRun()
	if directErr != nil {
		return directRes, true, directErr
	}
	PrintOK("AWG Manager direct terminal bootstrap from operator PC succeeded")
	return directRes, false, nil
}

func shouldTryDirectAWGMBootstrapFallback(err error) bool {
	if err == nil || isAWGMUnauthorized(err) {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "bootstrap script failed") {
		return false
	}
	if strings.Contains(s, "awgm vps relay failed") {
		return true
	}
	return shouldTryDirectAWGMPreflightFallback(err)
}

func probeAWGMSystemInfoWithDirectFallback(viaVPS bool, vpsProbe, directProbe awgmSystemInfoProbe) (*AWGMSystemInfo, bool, error) {
	if !viaVPS {
		info, err := directProbe()
		return info, false, err
	}
	info, err := vpsProbe()
	if err == nil {
		return info, true, nil
	}
	if !shouldTryDirectAWGMPreflightFallback(err) {
		return nil, true, err
	}
	PrintWarn("AWG Manager VPS relay preflight failed at the web edge (" + shortAWGMErrorForOperator(err) + "); trying direct from this operator PC before defer/cancel")
	info, directErr := directProbe()
	if directErr != nil {
		return nil, true, directErr
	}
	PrintOK("AWG Manager direct preflight from operator PC succeeded; terminal bootstrap will use direct path")
	return info, false, nil
}

func shouldTryDirectAWGMPreflightFallback(err error) bool {
	if err == nil || isAWGMUnauthorized(err) {
		return false
	}
	var httpErr *AWGMHTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 403, 502, 503, 504:
			return true
		}
	}
	s := strings.ToLower(err.Error())
	for _, needle := range []string{"http 403", "http 502", "http 503", "http 504"} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return isAWGMTransientUnavailable(err)
}

const awgmRelayJSONMarker = "__WG_MONITOR_JSON__"

func parseAWGMSystemInfoRelayOutput(out string) (*AWGMSystemInfo, error) {
	idx := strings.LastIndex(out, awgmRelayJSONMarker)
	if idx < 0 {
		return nil, fmt.Errorf("awgm vps relay returned no system info marker")
	}
	raw := strings.TrimSpace(out[idx+len(awgmRelayJSONMarker):])
	if nl := strings.IndexByte(raw, '\n'); nl >= 0 {
		raw = strings.TrimSpace(raw[:nl])
	}
	var env awgmEnvelope[AWGMSystemInfo]
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("awgm vps relay system info decode: %w", err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgm vps relay system info: success=false")
	}
	return &env.Data, nil
}

func shouldRunAWGMBootstrapViaVPS(state *State, rawURL string) bool {
	return state != nil && strings.TrimSpace(state.Backend.Host) != "" && !isLocalAWGMURL(rawURL)
}

func isLocalAWGMURL(rawURL string) bool {
	u, err := url.Parse(normalizeAWGMURL(rawURL))
	if err != nil {
		return false
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

var relayNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func safeRelayName(name string) string {
	name = relayNameRe.ReplaceAllString(strings.TrimSpace(name), "_")
	name = strings.Trim(name, "_-")
	if name == "" {
		return "agent"
	}
	if len(name) > 32 {
		return name[:32]
	}
	return name
}

var awgmVPSRelayPython = awgmrelay.ScriptString
