package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// actionUpdateComponents replaces the old "[2] Обновить бэкенд" and
// "[4] Обновить агента" menu items with one flow:
//
//  1. Probe the latest GitHub release.
//  2. Refresh installed versions from live backend/VPS endpoints.
//  3. Show a per-component status table (backend + every agent), marking
//     outdated rows.
//  4. Ask which to update.
//  5. For each chosen target, render an explicit context block (alias,
//     address, last deploy, current public IP of the operator — to spot
//     "wrong SSTP active") and require an explicit y/N before any SSH.
//  6. TCP-probe the target before SSH and abort early when unreachable
//     ("VPN не подключён?").
//  7. Delegate to the existing actionUpdateBackend / actionUpdateAgent.
func actionUpdateComponents(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		PrintFail("wizard.toml пуст — сначала установи бэкенд и/или агенты")
		return fmt.Errorf("nothing to update")
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)
	fmt.Println()

	refreshInstalledComponentState(state, secrets)
	targets := buildUpdateTargets(state, rel.TagName)
	printUpdateStatusTable(targets)

	outdated := filterOutdated(targets)
	if len(outdated) == 0 {
		if blocked := filterUpdateBlocked(targets); len(blocked) > 0 {
			PrintWarn(fmt.Sprintf("есть %d компонент(а) с repair metadata — сначала sync-vps / re-enroll, потом обновление", len(blocked)))
			return nil
		}
		PrintOK("всё актуально, обновлять нечего")
		return nil
	}

	choice := askUpdateChoice(targets, outdated)
	if len(choice) == 0 {
		PrintInfo("ничего не выбрано — выход")
		return nil
	}

	pubIP := currentPublicIP()
	if pubIP == "" {
		PrintWarn("не смог определить твой внешний IP — индикатор активного SSTP недоступен")
	}

	for _, t := range choice {
		fmt.Println()
		if !confirmTargetContext(t, pubIP) {
			PrintWarn("пропущено: " + t.Label)
			continue
		}
		if shouldProbeReachabilityBeforeUpdate(state, secrets, t) {
			if !probeReachable(t.Host, t.Port, 3*time.Second) {
				PrintFail(updateTargetReachabilityFailureMessage(t))
				continue
			}
		}
		if err := runOneUpdate(state, secrets, dl, t); err != nil {
			PrintFail(t.Label + ": " + err.Error())
		}
	}
	return nil
}

func shouldProbeReachabilityBeforeUpdate(state *State, secrets *SecretStore, t updateTarget) bool {
	if !t.IsAgent {
		return false
	}
	if strings.TrimSpace(t.Host) == "" {
		return false
	}
	if os.Getenv("WG_NO_PULL") != "1" && canPullDeploy(state, secrets, t) {
		return false
	}
	if shouldRecoverViaAWGMBeforeLocalProbe(state, t) {
		return false
	}
	return true
}

func shouldRecoverViaAWGMBeforeLocalProbe(state *State, t updateTarget) bool {
	if state == nil || !t.IsAgent {
		return false
	}
	if strings.TrimSpace(t.InstalledVersion) == "" || needsBootstrapForSelfUpdateNohupBug(t) {
		return true
	}
	ag := state.FindAgent(t.AgentNickname)
	if ag == nil {
		return false
	}
	return strings.TrimSpace(ag.AWGMURL) != "" || strings.EqualFold(strings.TrimSpace(ag.DeployMode), "awgm")
}

// updateTarget describes one row in the update table and is consumed by the
// runner to dispatch the right install action.
type updateTarget struct {
	Label               string // e.g. "backend (vps.example.com)" or "agent testkeen"
	IsAgent             bool   // false → backend, true → an agent
	AgentNickname       string // populated when IsAgent
	Kind                string
	Ring                string
	Host                string
	Port                int
	DisplayEndpoint     string
	NetworkLabel        string
	InstalledVersion    string // last deployed version recorded in wizard.toml
	LatestVersion       string // GitHub latest tag
	LastDeploy          string
	PendingVersion      string
	PendingSince        string
	PendingCurrent      bool
	NeedsUpdate         bool
	UpdateBlockedReason string
}

func buildUpdateTargets(state *State, latest string) []updateTarget {
	var out []updateTarget
	if state.Backend.Host != "" {
		port := state.Backend.Port
		if port == 0 {
			port = 22
		}
		out = append(out, updateTarget{
			Label:            "backend (" + nonEmpty(state.Backend.Domain, state.Backend.Host) + ")",
			Host:             state.Backend.Host,
			Port:             port,
			DisplayEndpoint:  fmt.Sprintf("%s:%d", state.Backend.Host, port),
			NetworkLabel:     "Интернет (публичный VPS)",
			InstalledVersion: state.Backend.LastDeployedVersion,
			LatestVersion:    latest,
			LastDeploy:       state.Backend.LastDeploy,
			NeedsUpdate:      state.Backend.LastDeployedVersion != latest,
		})
	}
	for _, a := range state.Agents {
		pendingCurrent := a.PendingVersion != "" && a.PendingVersion == latest
		blockedReason := agentUpdateBlockedReason(a)
		out = append(out, updateTarget{
			Label:               "agent " + a.Nickname,
			IsAgent:             true,
			AgentNickname:       a.Nickname,
			Kind:                nonEmpty(a.Kind, "static"),
			Ring:                rolloutRing(a.Ring),
			Host:                a.Host,
			Port:                portOrDefault(a.Port, 222),
			DisplayEndpoint:     agentStatusEndpointLabel(a),
			NetworkLabel:        agentUpdateNetworkLabel(a),
			InstalledVersion:    a.LastDeployedVersion,
			LatestVersion:       latest,
			LastDeploy:          a.LastDeploy,
			PendingVersion:      a.PendingVersion,
			PendingSince:        a.PendingSince,
			PendingCurrent:      pendingCurrent,
			NeedsUpdate:         a.LastDeployedVersion != latest && !pendingCurrent && blockedReason == "",
			UpdateBlockedReason: blockedReason,
		})
	}
	return out
}

func agentUpdateBlockedReason(a AgentState) string {
	if !agentHasDeployMetadataGap(&a) {
		return ""
	}
	return "metadata-gap: сначала sync-vps; если backend тоже пустой — re-enroll через AWG Manager"
}

func agentUpdateNetworkLabel(a AgentState) string {
	if strings.TrimSpace(a.Host) != "" {
		return "LAN (требуется активный SSTP/VPN до клиента)"
	}
	if strings.EqualFold(strings.TrimSpace(a.DeployMode), "awgm") || strings.TrimSpace(a.AWGMURL) != "" {
		return "AWG Manager / VPS pull-flow (локальный SSH endpoint не нужен)"
	}
	if agentHasDeployMetadataGap(&a) {
		return "metadata-gap (сначала sync-vps; если backend пустой — re-enroll через AWG Manager)"
	}
	return "локальный SSH endpoint неизвестен"
}

type updateRefreshSummary struct {
	VPSChecked         bool
	BackendVersion     string
	AgentsAdded        []string
	AgentsDiverged     []string
	AgentsMACConflicts []string
	VPSError           error
}

func refreshInstalledComponentState(state *State, secrets *SecretStore) updateRefreshSummary {
	var summary updateRefreshSummary
	if state == nil {
		return summary
	}
	refreshBackendInstalledVersion(state)
	summary.BackendVersion = state.Backend.LastDeployedVersion

	token := ""
	if secrets != nil {
		token = secrets.GetNonInteractive("WIZARD_TOKEN")
	}
	c := NewResilientVPSClientForBackend(state, secrets, token, 5*time.Second)
	if c == nil {
		return summary
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	remote, err := c.ListAgents(ctx)
	if err != nil {
		summary.VPSError = err
		PrintWarn("VPS sync перед проверкой обновлений не прошёл: " + err.Error())
		return summary
	}
	merged, added, divergent, macConflicts := MergeAgents(state.Agents, remote)
	state.Agents = merged
	summary.VPSChecked = true
	summary.AgentsAdded = added
	summary.AgentsDiverged = divergent
	summary.AgentsMACConflicts = macConflicts
	if len(added) > 0 {
		PrintInfo(fmt.Sprintf("VPS sync: добавлено из backend: %s", strings.Join(added, ", ")))
	}
	if len(divergent) > 0 {
		PrintWarn(fmt.Sprintf("VPS sync: SSH-адреса отличаются от локальных для: %s", strings.Join(divergent, ", ")))
	}
	if len(macConflicts) > 0 {
		PrintWarn(fmt.Sprintf("VPS sync: expected_mac отличается на VPS; локальный pin сохранён для: %s", strings.Join(macConflicts, ", ")))
	}
	return summary
}

func refreshBackendInstalledVersion(state *State) {
	if state == nil || state.Backend.Domain == "" {
		return
	}
	version := fetchBackendHealthVersion(state.Backend.Domain, state.Backend.Host)
	if version == "" {
		return
	}
	state.Backend.LastDeployedVersion = version
}

func fetchBackendHealthVersion(domain, dialHost string) string {
	base := strings.TrimSpace(domain)
	if base == "" {
		return ""
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	url := strings.TrimRight(base, "/") + "/healthz"
	cli := &http.Client{Timeout: 3 * time.Second}
	if strings.TrimSpace(dialHost) != "" {
		cli = NewVPSClientWithTimeoutAndDialHost(domain, "healthz", 3*time.Second, dialHost).HTTP
	}
	resp, err := cli.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return ""
	}
	return strings.TrimSpace(out.Version)
}

func filterOutdated(all []updateTarget) []updateTarget {
	var out []updateTarget
	for _, t := range all {
		if t.NeedsUpdate {
			out = append(out, t)
		}
	}
	return out
}

func filterUpdateBlocked(all []updateTarget) []updateTarget {
	var out []updateTarget
	for _, t := range all {
		if t.UpdateBlockedReason != "" {
			out = append(out, t)
		}
	}
	return out
}

func printUpdateStatusTable(rows []updateTarget) {
	fmt.Println(Colorize("Состояние компонентов:", ColorBold))
	for _, t := range rows {
		installed := t.InstalledVersion
		if installed == "" {
			installed = "(неизвестно)"
		}
		extra := ""
		if t.IsAgent {
			extra = updateTargetExtraLabel(t)
		}
		fmt.Printf("  • %-30s %-15s %s%s\n", t.Label, installed, updateTargetStatusLabel(t), extra)
	}
	fmt.Println()
}

func updateTargetStatusLabel(t updateTarget) string {
	switch {
	case t.UpdateBlockedReason != "":
		return Colorize("[нужен repair metadata]", ColorYellow)
	case t.PendingCurrent:
		return Colorize("[ждёт wake → "+t.PendingVersion+"]", ColorYellow)
	case t.NeedsUpdate && t.InstalledVersion == "":
		return Colorize("[версия неизвестна]", ColorYellow)
	case t.NeedsUpdate:
		return Colorize("[обновить → "+t.LatestVersion+"]", ColorYellow)
	default:
		return Colorize("[актуально]", ColorGreen)
	}
}

func updateTargetExtraLabel(t updateTarget) string {
	if !t.IsAgent {
		return ""
	}
	parts := []string{
		"endpoint=" + updateTargetEndpointLabel(t),
		"kind=" + nonEmpty(t.Kind, "static"),
		"ring=" + rolloutRing(t.Ring),
	}
	if t.PendingVersion != "" {
		parts = append(parts, "pending="+t.PendingVersion)
	}
	if t.UpdateBlockedReason != "" {
		parts = append(parts, "blocked=metadata-gap")
	}
	return " " + strings.Join(parts, " ")
}

// askUpdateChoice presents:
//
//	[a] всё, что устарело
//	[b] backend
//	[c] agent testkeen
//	...
//	[Enter] выход
//
// Multi-select via space/comma is supported (e.g. "b c"). Returns empty
// slice when the user hits Enter.
func askUpdateChoice(all []updateTarget, outdated []updateTarget) []updateTarget {
	if len(outdated) == 0 {
		return nil
	}
	type opt struct {
		key string
		t   *updateTarget
	}
	var opts []opt
	keys := []byte{'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm', 'n', 'o', 'p'}
	idx := 0
	for i := range all {
		t := &all[i]
		if !t.NeedsUpdate {
			continue
		}
		if idx >= len(keys) {
			break
		}
		opts = append(opts, opt{string(keys[idx]), t})
		idx++
	}
	fmt.Println(Colorize("Что обновить?", ColorBold))
	fmt.Printf("  [a] всё, что устарело (%d)\n", len(outdated))
	for _, o := range opts {
		fmt.Printf("  [%s] %s\n", o.key, updateTargetChoiceLabel(*o.t))
	}
	fmt.Println("  [Enter] ничего, выход")
	fmt.Print("> ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return nil
	}
	if line == "a" {
		return outdated
	}
	tokens := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == ';' })
	seen := map[string]bool{}
	var picked []updateTarget
	for _, tok := range tokens {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		for _, o := range opts {
			if o.key == tok {
				picked = append(picked, *o.t)
			}
		}
	}
	if len(picked) == 0 {
		PrintWarn("не понял выбор: " + line)
	}
	return picked
}

func updateTargetChoiceLabel(t updateTarget) string {
	if !t.IsAgent {
		return t.Label
	}
	return fmt.Sprintf("%s (%s)", t.Label, updateTargetEndpointLabel(t))
}

// confirmTargetContext renders a one-screen summary of WHERE we're about to
// SSH and WHAT we'll change, then asks for explicit y/N. The aim is to make
// "забыл переключить SSTP, сейчас засунем агента клиента А на роутер
// клиента Б" require typing 'y' to happen.
func confirmTargetContext(t updateTarget, currentIP string) bool {
	fmt.Println(Colorize("─────────────────────────────────────────────", ColorCyan))
	fmt.Printf("%s %s\n", Colorize("Цель:", ColorBold), t.Label)
	fmt.Printf("  Адрес       : %s\n", updateTargetEndpointLabel(t))
	fmt.Printf("  Сеть        : %s\n", updateTargetNetworkLabel(t))
	fmt.Printf("  Установлено : %s\n", nonEmpty(t.InstalledVersion, "(неизвестно)"))
	fmt.Printf("  Будет       : %s\n", t.LatestVersion)
	fmt.Printf("  Last deploy : %s\n", nonEmpty(t.LastDeploy, "—"))
	if currentIP != "" {
		fmt.Printf("  Твой IP     : %s\n", currentIP)
	} else {
		fmt.Printf("  Твой IP     : %s\n", Colorize("не определён", ColorDim))
	}
	fmt.Println(Colorize("─────────────────────────────────────────────", ColorCyan))
	if os.Getenv("WG_YES_TO_ALL") == "1" {
		PrintWarn("WG_YES_TO_ALL=1 → пропускаю подтверждение")
		return true
	}
	resp := strings.ToLower(strings.TrimSpace(Ask("Подтвердить и продолжить? [y/N]", "")))
	return resp == "y" || resp == "yes" || resp == "д" || resp == "да"
}

func updateTargetEndpointLabel(t updateTarget) string {
	if strings.TrimSpace(t.DisplayEndpoint) != "" {
		return t.DisplayEndpoint
	}
	if strings.TrimSpace(t.Host) == "" {
		return "-"
	}
	return fmt.Sprintf("%s:%d", strings.TrimSpace(t.Host), portOrDefault(t.Port, 222))
}

func updateTargetNetworkLabel(t updateTarget) string {
	if strings.TrimSpace(t.NetworkLabel) != "" {
		return t.NetworkLabel
	}
	if t.IsAgent {
		return "LAN (требуется активный SSTP/VPN до клиента)"
	}
	return "Интернет (публичный VPS)"
}

func updateTargetReachabilityFailureMessage(t updateTarget) string {
	return fmt.Sprintf("%s не отвечает за 3с — проверь SSTP/VPN, либо вручную исправь host/port в wizard.toml", updateTargetEndpointLabel(t))
}

// runOneUpdate dispatches the right per-component install action. Agents
// with a known-good prior deploy go through the pull-based self_update
// path via VPS (no SSH required), eliminating the 192.168.31.1 LAN-IP
// collision that plagues the SSH route. Cold installs, pre-rc32 agents, and
// recoverable pull-flow failures stay in this menu: the wizard asks for any
// missing AWG Manager/KeenDNS data and runs the existing bootstrap/re-enroll
// flow. Legacy SSH is break-glass only.
var installAgentAWGMForUpdate = actionInstallAgentAWGM

func runOneUpdate(state *State, secrets *SecretStore, dl *Downloader, t updateTarget) error {
	if !t.IsAgent {
		return actionUpdateBackend(state, secrets, dl)
	}
	if os.Getenv("WG_ALLOW_BROKEN_SELF_UPDATE") != "1" && needsBootstrapForSelfUpdateNohupBug(t) {
		if handled, err := recoverAgentUpdateViaAWGM(state, secrets, dl, t,
			fmt.Sprintf("%s стоит на %s: self_update до v0.13.0-rc32 может подтвердить ACK, но не выполнить swap на Keenetic без nohup", t.AgentNickname, t.InstalledVersion)); handled {
			return err
		}
		return fmt.Errorf("%s стоит на %s: pull-flow небезопасен из-за старого self_update/nohup, а локального agent state нет", t.AgentNickname, t.InstalledVersion)
	}
	if os.Getenv("WG_NO_PULL") != "1" && canPullDeploy(state, secrets, t) {
		PrintInfo(fmt.Sprintf("pull-flow через VPS (без SSH) для %s → %s", t.AgentNickname, t.LatestVersion))
		if err := runPullDeploy(state, secrets, t); err == nil {
			return nil
		} else {
			PrintWarn("pull-flow failed: " + err.Error())
			if shouldFallbackToAWGMReinstall(err) {
				if handled, recoverErr := recoverAgentUpdateViaAWGM(state, secrets, dl, t,
					"старый агент может не уметь backend mirror/self-update"); handled {
					return recoverErr
				}
			}
			if shouldOfferAWGMReenrollFromPullError(err) && askYesNo("Сделать re-enroll через AWG Manager сейчас?", true) {
				if handled, recoverErr := recoverAgentUpdateViaAWGM(state, secrets, dl, t,
					"pull-flow выглядит как рассинхрон agent token/cmdloop"); handled {
					return recoverErr
				}
			}
			if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
				return fmt.Errorf("pull-flow failed; SSH fallback скрыт, потому что новый deploy идёт через VPS/AWG Manager: %w", err)
			}
			if !askYesNo("Откатиться на legacy SSH-путь?", false) {
				return err
			}
		}
	}
	if strings.TrimSpace(t.InstalledVersion) == "" {
		if handled, err := recoverAgentUpdateViaAWGM(state, secrets, dl, t,
			fmt.Sprintf("%s ещё не имеет подтверждённой установленной версии; нужен bootstrap/install вместо binary-swap update", t.AgentNickname)); handled {
			return err
		}
	}
	if strings.TrimSpace(t.Host) == "" {
		if ag := state.FindAgent(t.AgentNickname); ag != nil &&
			(strings.TrimSpace(ag.AWGMURL) != "" || strings.EqualFold(strings.TrimSpace(ag.DeployMode), "awgm")) {
			if handled, err := recoverAgentUpdateViaAWGM(state, secrets, dl, t,
				fmt.Sprintf("%s не имеет локального SSH endpoint; обновление должно идти через AWG Manager/VPS flow", t.AgentNickname)); handled {
				return err
			}
		}
		return fmt.Errorf("%s metadata-gap: нет SSH endpoint и нет AWG Manager URL. Сначала sync-vps; если backend тоже пустой, сделай re-enroll через AWG Manager", t.AgentNickname)
	}
	return actionUpdateAgent(state, secrets, dl, t.AgentNickname)
}

func recoverAgentUpdateViaAWGM(state *State, secrets *SecretStore, dl *Downloader, t updateTarget, reason string) (bool, error) {
	if state == nil || !t.IsAgent {
		return false, nil
	}
	ag := state.FindAgent(t.AgentNickname)
	if ag == nil {
		return false, nil
	}
	if strings.TrimSpace(reason) != "" {
		PrintWarn(reason)
	}
	if strings.TrimSpace(ag.AWGMURL) == "" {
		PrintInfo("Для продолжения нужен AWG Manager/KeenDNS URL. Введи его здесь, и wizard продолжит без перехода в другой пункт меню.")
		if os.Getenv("WG_YES_TO_ALL") != "1" {
			ag.AWGMURL = normalizeAWGMURL(Ask("AWG Manager URL для "+ag.Nickname+" (KeenDNS, https://...; Enter — отмена)", ag.AWGMURL))
		}
	}
	ag.AWGMURL = normalizeAWGMURL(ag.AWGMURL)
	if ag.AWGMURL == "" {
		return true, fmt.Errorf("%s: AWG Manager URL required for bootstrap/reinstall", ag.Nickname)
	}
	ag.DeployMode = "awgm"
	if strings.TrimSpace(ag.AWGMAuth) == "" {
		ag.AWGMAuth = "router-admin"
	}
	PrintWarn("перехожу на AWG Manager reinstall/bootstrap: " + ag.Nickname)
	return true, installAgentAWGMForUpdate(state, secrets, dl, ag.Nickname)
}

const selfUpdateNohupFixVersion = "v0.13.0-rc32"

func needsBootstrapForSelfUpdateNohupBug(t updateTarget) bool {
	if !t.IsAgent || t.InstalledVersion == "" {
		return false
	}
	if compareReleaseTags(t.LatestVersion, selfUpdateNohupFixVersion) < 0 {
		return false
	}
	return compareReleaseTags(t.InstalledVersion, selfUpdateNohupFixVersion) < 0
}

func shouldOfferAWGMReenrollFromPullError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"repair-agent-token",
		"token-not-found",
		"token-corrupt",
		"рассинхрон",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func shouldFallbackToAWGMReinstall(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"pull deploy preflight",
		"token-corrupt",
		"rassync",
		"рассинхрон",
		"stale",
		"heartbeat never",
		"не забирает команды",
	} {
		if strings.Contains(msg, marker) {
			return false
		}
	}
	return true
}

// canPullDeploy reports whether the wizard has everything it needs to run
// the VPS-mediated self_update flow for this target: a backend domain, a
// wizard token in the secret store, and a recorded LastDeployedVersion
// (proves the agent has been deployed at least once and the long-poll
// channel is alive). A fresh agent with no version recorded must go
// through the SSH cold-install path.
func canPullDeploy(state *State, secrets *SecretStore, t updateTarget) bool {
	if state.Backend.Domain == "" {
		return false
	}
	if t.InstalledVersion == "" {
		return false
	}
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	return tok != ""
}

func shortCommandID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func backendReleaseAssetURL(state *State, version, asset string) string {
	if state == nil || strings.TrimSpace(state.Backend.Domain) == "" {
		return ""
	}
	raw := strings.TrimRight(strings.TrimSpace(state.Backend.Domain), "/")
	scheme := "https"
	host := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		scheme = u.Scheme
		host = u.Host
	} else if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	base := scheme + "://" + host
	return base + "/v1/releases/download/" + url.PathEscape(version) + "/" + url.PathEscape(asset)
}

func releaseAssetURLForRouter(state *State, version, asset, fallback string) string {
	if u := backendReleaseAssetURL(state, version, asset); u != "" {
		return u
	}
	return fallback
}

// runPullDeploy drives the pull-based update for a single agent. It enqueues
// a self_update command on the VPS, waits for the agent's CommandResult
// (verify+swap-schedule ack), then polls /v1/wizard/agents up to 6 minutes
// for the heartbeat-side version flip that confirms the new binary is
// actually running. Returns an error on any failure of those three legs.
func runPullDeploy(state *State, secrets *SecretStore, t updateTarget) error {
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	c := NewResilientVPSClientForBackend(state, secrets, tok, 8*time.Second)
	if c == nil {
		return fmt.Errorf("VPSClient unavailable")
	}
	if err := ensurePullDeployReady(c, t); err != nil {
		if scheduled, scheduleErr := scheduleDeferredAgentUpdateIfWanted(state, secrets, c, t, err); scheduled {
			return scheduleErr
		}
		return err
	}

	cmdID, err := deployWithRetry(c, t.AgentNickname, t.LatestVersion)
	if err != nil {
		return fmt.Errorf("enqueue deploy: %w", err)
	}
	PrintOK(fmt.Sprintf("команда отправлена (cmd_id=%s) — жду ack от агента", shortCommandID(cmdID)))

	// Phase 1: agent CommandResult. Verify+schedule typically completes in
	// 5-30s (download ~12MB on a Keenetic over the operator's link). Allow
	// up to 90s via two 45-second long-polls.
	deadline := time.Now().Add(90 * time.Second)
	var res *wire.CommandResult
	for time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(context.Background(), 65*time.Second)
		r, err := c.AwaitCommandResult(pollCtx, t.AgentNickname, cmdID, 45)
		pollCancel()
		if err != nil {
			return fmt.Errorf("await result: %w", err)
		}
		if r != nil {
			res = r
			break
		}
	}
	if res == nil {
		if err := markPendingOnMobileAckTimeout(state, t, time.Now().UTC().Format(time.RFC3339)); err == nil {
			if ag := state.FindAgent(t.AgentNickname); ag != nil {
				pushCtx, pushCancel := context.WithTimeout(context.Background(), 25*time.Second)
				if pushErr := c.PushAgent(pushCtx, AgentStateToRemote(*ag)); pushErr != nil {
					PrintWarn("VPS pending sync failed: " + pushErr.Error())
				}
				pushCancel()
			}
			PrintWarn("мобильный агент не подтвердил команду сейчас — пометил обновление как pending до следующего wake")
			return nil
		}
		return fmt.Errorf("агент не подтвердил команду за 90с%s", pullDeployNoAckHint(c, t))
	}
	if res.Status != "ok" {
		return fmt.Errorf("агент вернул %s: %s", res.Status, res.Output)
	}
	PrintOK("агент подтвердил " + strings.TrimSpace(res.Output))

	// Phase 2: heartbeat version-flip. After a successful ACK the new process
	// should report immediately on start. Keep this wait short so one router
	// with a slow/missing heartbeat does not stall a whole bulk update; if the
	// flip does not arrive, record pending_version and let sync/doctor confirm
	// it later.
	heartbeatWait := pullDeployHeartbeatWait(t)
	PrintInfo(fmt.Sprintf("жду подтверждения новой версии через heartbeat (до %s)", formatDurationRU(heartbeatWait)))
	flipStarted := time.Now()
	nextProgress := flipStarted.Add(15 * time.Second)
	flipDeadline := flipStarted.Add(heartbeatWait)
	for time.Now().Before(flipDeadline) {
		time.Sleep(5 * time.Second)
		listCtx, listCancel := context.WithTimeout(context.Background(), 20*time.Second)
		remotes, err := c.ListAgents(listCtx)
		listCancel()
		if err != nil {
			PrintWarn("VPS list failed: " + err.Error())
			continue
		}
		for _, r := range remotes {
			if r.Nickname == t.AgentNickname && r.LastDeployedVersion == t.LatestVersion {
				PrintOK(fmt.Sprintf("✅ %s → %s подтверждён heartbeat'ом", t.AgentNickname, t.LatestVersion))
				if a := state.FindAgent(t.AgentNickname); a != nil {
					a.LastDeploy = time.Now().UTC().Format(time.RFC3339)
					a.LastDeployedVersion = t.LatestVersion
					a.PendingVersion = ""
					a.PendingSince = ""
					pushCtx, pushCancel := context.WithTimeout(context.Background(), 25*time.Second)
					if pushErr := c.PushAgent(pushCtx, AgentStateToRemote(*a)); pushErr != nil {
						PrintWarn("VPS version sync failed: " + pushErr.Error())
					}
					pushCancel()
				}
				return nil
			}
		}
		if now := time.Now(); !now.Before(nextProgress) {
			PrintInfo(fmt.Sprintf("heartbeat ещё не показал %s на %s, жду...", t.LatestVersion, t.AgentNickname))
			nextProgress = now.Add(15 * time.Second)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := markPendingOnHeartbeatTimeout(state, t, now); err != nil {
		return err
	}
	if ag := state.FindAgent(t.AgentNickname); ag != nil {
		pushCtx, pushCancel := context.WithTimeout(context.Background(), 25*time.Second)
		if pushErr := c.PushAgent(pushCtx, AgentStateToRemote(*ag)); pushErr != nil {
			PrintWarn("VPS pending sync failed: " + pushErr.Error())
		}
		pushCancel()
	}
	PrintWarn(fmt.Sprintf("ACK получен, но heartbeat ещё не подтвердил %s — пометил как pending и продолжаю; проверь позже через sync/doctor", t.LatestVersion))
	return nil
}

func ensurePullDeployReady(c *VPSClient, t updateTarget) error {
	if c == nil || strings.EqualFold(t.Kind, "mobile") {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("pull deploy preflight: не удалось проверить VPS heartbeat: %w", err)
	}
	return pullDeployReadinessFromAgents(agents, t, time.Now())
}

func pullDeployHeartbeatWait(t updateTarget) time.Duration {
	if strings.EqualFold(t.Kind, "mobile") {
		return 90 * time.Second
	}
	return 90 * time.Second
}

func markPendingOnHeartbeatTimeout(state *State, t updateTarget, since string) error {
	if state == nil {
		return fmt.Errorf("state unavailable")
	}
	ag := state.FindAgent(t.AgentNickname)
	if ag == nil {
		return fmt.Errorf("agent %s not found in local state", t.AgentNickname)
	}
	ag.PendingVersion = t.LatestVersion
	ag.PendingSince = since
	return nil
}

func formatDurationRU(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d мин", int(d/time.Minute))
	}
	if d > time.Minute {
		return fmt.Sprintf("%dс", int(d/time.Second))
	}
	return fmt.Sprintf("%dс", int(d/time.Second))
}

func pullDeployReadinessFromAgents(agents []RemoteAgent, t updateTarget, now time.Time) error {
	if strings.EqualFold(t.Kind, "mobile") {
		return nil
	}
	nick := strings.TrimSpace(t.AgentNickname)
	for _, a := range agents {
		if a.Nickname != nick {
			continue
		}
		hb := formatHeartbeatStatus(a.LastSeenAt, now)
		if strings.HasPrefix(hb, "fresh") {
			return nil
		}
		return fmt.Errorf("pull deploy preflight: %s", strings.TrimPrefix(pullDeployNoAckHintFromAgents(agents, t, now), "; "))
	}
	return fmt.Errorf("pull deploy preflight: %s", strings.TrimPrefix(pullDeployNoAckHintFromAgents(agents, t, now), "; "))
}

func deployWithRetry(c *VPSClient, nickname, version string) (string, error) {
	var last error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmdID, err := c.Deploy(ctx, nickname, version)
		cancel()
		if err == nil {
			return cmdID, nil
		}
		last = err
		if attempt < 3 {
			PrintWarn(fmt.Sprintf("enqueue deploy timeout/error, retry %d/3 через 2s: %v", attempt+1, err))
			time.Sleep(2 * time.Second)
		}
	}
	return "", last
}

func rolloutRing(ring string) string {
	switch strings.ToLower(strings.TrimSpace(ring)) {
	case "canary", "beta", "stable":
		return strings.ToLower(strings.TrimSpace(ring))
	default:
		return "stable"
	}
}

func markPendingOnMobileAckTimeout(state *State, t updateTarget, now string) error {
	if !t.IsAgent || t.Kind != "mobile" {
		return fmt.Errorf("non-mobile pull timeout")
	}
	ag := state.FindAgent(t.AgentNickname)
	if ag == nil {
		return fmt.Errorf("agent %q not found", t.AgentNickname)
	}
	ag.PendingVersion = t.LatestVersion
	ag.PendingSince = now
	return nil
}

func pullDeployNoAckHint(c *VPSClient, t updateTarget) string {
	if c == nil {
		return " (VPSClient unavailable)"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return "; не удалось перечитать VPS heartbeat: " + err.Error()
	}
	return pullDeployNoAckHintFromAgents(agents, t, time.Now())
}

func pullDeployNoAckHintFromAgents(agents []RemoteAgent, t updateTarget, now time.Time) string {
	nick := strings.TrimSpace(t.AgentNickname)
	for _, a := range agents {
		if a.Nickname != nick {
			continue
		}
		hb := formatHeartbeatStatus(a.LastSeenAt, now)
		switch {
		case hb == "never" || strings.HasPrefix(hb, "stale"):
			return fmt.Sprintf("; VPS heartbeat %s — агент не забирает команды, состояние похоже на token-corrupt/рассинхрон. Частая причина: на роутере старый/чужой agent token (backend пишет token-not-found); wizard предложит re-enroll через AWG Manager прямо здесь", hb)
		case strings.HasPrefix(hb, "fresh"):
			return fmt.Sprintf("; VPS heartbeat %s, но команда не завершилась — проверь cmdloop на агенте; если в backend journal есть token-not-found, wizard предложит re-enroll через AWG Manager прямо здесь", hb)
		default:
			return fmt.Sprintf("; VPS heartbeat %s — проверь agent service; при token/cmdloop рассинхроне wizard предложит re-enroll через AWG Manager прямо здесь", hb)
		}
	}
	return fmt.Sprintf("; агент %s не найден в /v1/wizard/agents — обнови состояние из VPS или введи AWG Manager URL здесь, если нужен bootstrap/re-enroll", nick)
}

// askYesNo prints `prompt [Y/n]: ` (capitalised default) and returns true
// for empty/y/yes, false for n/no. Wizard-style fallback prompt used after
// recoverable failures.
func askYesNo(prompt string, defaultYes bool) bool {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	resp := strings.ToLower(strings.TrimSpace(Ask(prompt+" "+hint, "")))
	if resp == "" {
		return defaultYes
	}
	return resp == "y" || resp == "yes" || resp == "д" || resp == "да"
}

// currentPublicIP returns the operator's outbound IPv4 as seen by a public
// echo service. Best-effort — empty string on any failure (timeout, no net,
// service offline). The tunnel through which traffic leaves shapes this
// value, so if the operator switches SSTP between two clients, this number
// changes — that's the signal we surface to the operator before they
// confirm the SSH target.
func currentPublicIP() string {
	endpoints := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://checkip.amazonaws.com",
	}
	cli := &http.Client{Timeout: 3 * time.Second}
	for _, url := range endpoints {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "wg-monitor-deploy")
		resp, err := cli.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			return ip
		}
	}
	return ""
}

// probeReachable opens a plain TCP connection to host:port with the given
// timeout. Used to abort an update with a friendly "VPN не подключён?"
// before the SSH layer's password prompt sits there waiting for a TCP
// session that will never form.
func probeReachable(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
