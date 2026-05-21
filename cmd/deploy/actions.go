package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

// runPathDiscoveryStep is the operator-facing wrapper around
// stepFindReachablePath. Prints the candidate table, handles the
// multi-responder prompt, returns the cleanup func + chosen iface name
// (saved to wizard.toml as PreferredIface on success). Returns nil err +
// "" iface to signal "no path found" — caller delegates to
// diagnoseUnreachable for the cascade.
func runPathDiscoveryStep(host string, port int, preferred string, prober Prober) (*PathReport, func(), string, error) {
	target := fmt.Sprintf("%s:%d", host, port)
	PrintInfo("ищу " + target + " через все доступные интерфейсы (5с)...")
	rep, cleanup, err := stepFindReachablePath(prober, target, 5*time.Second)
	if err != nil {
		return nil, cleanup, "", err
	}
	fmt.Print(describePath(rep))
	if rep.Chosen == nil {
		return rep, cleanup, "", nil
	}
	preferredHit := preferred != "" && preferChosenCandidate(rep, preferred)
	if preferredHit {
		PrintInfo("preferred_iface " + preferred + " отвечает — использую его")
	}
	if rep.Multiple && !preferredHit && os.Getenv("WG_YES_TO_ALL") != "1" {
		fmt.Println("Несколько путей отвечают. Кого выбираешь?")
		responders := make([]PathCandidate, 0, len(rep.Candidates))
		for _, c := range rep.Candidates {
			if c.Responded() {
				responders = append(responders, c)
			}
		}
		for i, c := range responders {
			marker := "  "
			if rep.Chosen != nil && c.Iface == rep.Chosen.Iface {
				marker = "→ "
			}
			fmt.Printf("%s[%d] %s (%s, %dмс) [%s]\n", marker, i+1, c.Iface, c.LocalIP, c.Latency.Milliseconds(), c.Kind.String())
		}
		idx := parseIntOr(Ask("номер пути [1]", "1"), 1)
		if idx < 1 || idx > len(responders) {
			idx = 1
		}
		rep.Chosen = &responders[idx-1]
	}
	chosenName := ""
	if rep.Chosen != nil {
		chosenName = rep.Chosen.Iface
		// TODO(rc6): wire preferred fast-path — when preferred matches one of
		// the responding candidates, skip the full enumeration on subsequent
		// runs. Currently always does full probe; cache is write-only.
		_ = preferred
		PrintOK("использую " + chosenName + " (" + rep.Chosen.LocalIP + ", " + fmt.Sprint(rep.Chosen.Latency.Milliseconds()) + "мс)")
	}
	return rep, cleanup, chosenName, nil
}

func runPathDiscoveryStepWithNetfix(state *State, ag *AgentState, secrets *SecretStore) (*PathReport, func(), string, error) {
	port := portOrDefault(ag.Port, 222)
	rep, cleanup, iface, err := runPathDiscoveryStep(ag.Host, port, ag.PreferredIface, NewRealProber())
	if err != nil || rep == nil || rep.Chosen != nil {
		return rep, cleanup, iface, err
	}

	cand, ok := routeFixCandidate(rep)
	if !ok || os.Getenv("WG_YES_TO_ALL") == "1" {
		return rep, cleanup, iface, nil
	}

	targetIP, _, splitErr := net.SplitHostPort(rep.Target)
	if splitErr != nil {
		return rep, cleanup, iface, nil
	}
	fmt.Print(manualRouteHint(targetIP, cand))
	if !Confirm("Применить netfix сейчас и повторить поиск пути?", false) {
		return rep, cleanup, iface, nil
	}

	cleanup()
	cleanup = func() {}
	tok, addErr := addTempHostRoute(targetIP, cand.Index)
	if addErr != nil {
		PrintFail("netfix: " + addErr.Error())
		return rep, cleanup, iface, nil
	}
	routeCleanup := func() {
		_ = delTempHostRoute(tok)
	}
	PrintOK("netfix: /32 route добавлен, повторяю поиск пути")

	rep2, cleanup2, iface2, err2 := runPathDiscoveryStep(ag.Host, port, ag.PreferredIface, NewRealProber())
	combinedCleanup := func() {
		cleanup2()
		routeCleanup()
	}
	if err2 != nil || rep2 == nil || rep2.Chosen == nil {
		if rep2 != nil {
			PrintFail(diagnosisFromReport(rep2, heartbeatForAgent(state, ag, secrets)))
		}
		return rep2, combinedCleanup, iface2, err2
	}
	return rep2, combinedCleanup, iface2, nil
}

func routeFixCandidate(rep *PathReport) (PathCandidate, bool) {
	if rep == nil {
		return PathCandidate{}, false
	}
	for _, c := range rep.Candidates {
		if c.Kind != PathP2P || c.Index == 0 || c.Err == nil {
			continue
		}
		errText := strings.ToLower(c.Err.Error())
		if strings.Contains(errText, "route add") &&
			(strings.Contains(errText, "requires elevation") ||
				strings.Contains(errText, "access denied") ||
				strings.Contains(errText, "forbidden")) {
			return c, true
		}
	}
	return PathCandidate{}, false
}

// diagnoseUnreachable handles "Layer 1 found no responding path". Fetches
// VPS heartbeat for the agent (if state allows), classifies the candidate
// failure modes, prints a one-shot diagnostic, returns a generic err so
// the caller doesn't fall into SSH-retry loop.
func diagnoseUnreachable(state *State, ag *AgentState, rep *PathReport, secrets *SecretStore) error {
	PrintFail(diagnosisFromReport(rep, heartbeatForAgent(state, ag, secrets)))
	return fmt.Errorf("router %s unreachable", ag.Host)
}

func heartbeatForAgent(state *State, ag *AgentState, secrets *SecretStore) string {
	hb := ""
	if state.Backend.Domain != "" {
		if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
			if c := NewVPSClientForBackend(state, tok, 5*time.Second); c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				hb = c.HeartbeatStatus(ctx, ag.Nickname)
				cancel()
			}
		}
	}
	return hb
}

// diagnosisFromReport is the pure-logic core: takes a PathReport with no
// chosen candidate and an optional heartbeat status, returns a multi-line
// operator-readable diagnostic. Heartbeat empty → that branch silently
// dropped. Tested without I/O.
func diagnosisFromReport(rep *PathReport, hb string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("роутер %s недоступен ни через один из проверенных путей.\n", rep.Target))

	var hasP2PUp bool
	var refusedSeen, timeoutSeen bool
	for _, c := range rep.Candidates {
		if c.Kind == PathP2P {
			hasP2PUp = true
		}
		if c.Err != nil {
			s := strings.ToLower(c.Err.Error())
			switch {
			case strings.Contains(s, "refused"):
				refusedSeen = true
			case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
				timeoutSeen = true
			}
		}
	}
	if hint := routeElevationHint(rep); hint != "" {
		sb.WriteString(hint)
	}

	switch {
	case !hasP2PUp:
		sb.WriteString("  • у тебя нет ни одного UP VPN/SSTP интерфейса. Если ожидал, что роутер через тоннель — подними сначала клиент.\n")
	case timeoutSeen:
		var p2pName string
		for _, c := range rep.Candidates {
			if c.Kind == PathP2P {
				p2pName = c.Iface
				break
			}
		}
		sb.WriteString(fmt.Sprintf("  • %s up, но через него target не отвечает. Возможно сервер не маршрутизирует target, или удалённый firewall блокирует :222.\n", p2pName))
	}

	if refusedSeen {
		sb.WriteString("  • один из путей: порт закрыт (connection refused). SSH либо не на :222, либо firewall.\n")
	}

	switch {
	case strings.HasPrefix(hb, "fresh"):
		sb.WriteString(fmt.Sprintf("  • VPS heartbeat %s — роутер жив на сети, проблема в сетевом пути ОТ ТЕБЯ. Проверь активный SSTP/VPN.\n", hb))
	case strings.HasPrefix(hb, "stale"):
		sb.WriteString(fmt.Sprintf("  • VPS heartbeat %s — роутер давно не отчитывался, возможно выключен или агент упал.\n", hb))
	case hb == "never":
		sb.WriteString("  • VPS heartbeat: never — впервые ставим, нужен out-of-band доступ.\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

func routeElevationHint(rep *PathReport) string {
	if rep == nil {
		return ""
	}
	targetIP, _, err := net.SplitHostPort(rep.Target)
	if err != nil || targetIP == "" {
		return ""
	}
	for _, c := range rep.Candidates {
		if c.Kind != PathP2P || c.Index == 0 || c.Err == nil {
			continue
		}
		errText := strings.ToLower(c.Err.Error())
		if !strings.Contains(errText, "route add") {
			continue
		}
		if !strings.Contains(errText, "requires elevation") &&
			!strings.Contains(errText, "access denied") &&
			!strings.Contains(errText, "forbidden") {
			continue
		}
		return manualRouteHint(targetIP, c)
	}
	return ""
}

// ensureWizardToken returns a 64-hex token from the SecretStore, generating
// + persisting one if absent. Cached under key WIZARD_TOKEN.
func ensureWizardToken(secrets *SecretStore) (string, error) {
	if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
		return tok, nil
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(b[:])
	secrets.Set("WIZARD_TOKEN", tok)
	return tok, nil
}

// stepEnsureWizardSetup makes the VPS side ready for /v1/wizard/* endpoints,
// idempotently. Bridges the install-backend / update-backend gap: update path
// only swaps the binary, so a v0.11→v0.12 upgrade has the new code but no
// token file and no `wizard:` block in backend.yaml. This helper makes both
// ready in one shot; safe to call from both actionInstallBackend and
// actionUpdateBackend. Returns silently if everything is already in place.
//
// Caller is responsible for restarting the backend after this returns (the
// update + install paths already do that via stepUploadAndSwap / systemctl
// daemon-reload+restart).
func stepEnsureWizardSetup(s *SSH, secrets *SecretStore) error {
	tok, err := ensureWizardToken(secrets)
	if err != nil {
		return fmt.Errorf("ensure wizard token: %w", err)
	}
	// Mode 640: owner root (writable for ops), group wgmonitor (readable by
	// the backend process). Mode 600 would leave the wgmonitor group with no
	// access — backend would silently fail to load the token and /v1/wizard/*
	// endpoints would return 404. Symmetric to what bot-token.txt SHOULD have
	// (see DEPLOY-NN).
	if err := stepUploadFile(s, "/etc/wg-monitor/wizard-token.txt", []byte(tok+"\n"), "640"); err != nil {
		return fmt.Errorf("upload wizard-token.txt: %w", err)
	}
	if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/wizard-token.txt"); err != nil {
		PrintWarn("chown wizard-token.txt: " + err.Error())
	}
	// Probe backend.yaml for an existing wizard: block. grep returns 0 when
	// found; non-zero (typically 1) when not. Run swallows non-zero exit
	// codes into rc so this never errors out — we just check the value.
	_, _, rc, runErr := s.Run("grep -q '^wizard:' /etc/wg-monitor/backend.yaml")
	if runErr != nil {
		return fmt.Errorf("probe backend.yaml: %w", runErr)
	}
	if rc == 0 {
		// Block already present — nothing to do.
		return nil
	}
	PrintInfo("backend.yaml без wizard:-блока — добавляю")
	// Single-quoted heredoc — bash does NOT expand $/`` inside, so the
	// literal contents land verbatim. Leading blank line keeps separation
	// from whatever the file ends with.
	appendCmd := `cat >> /etc/wg-monitor/backend.yaml <<'EOF'

# Wizard sync token: enables /v1/wizard/agents read + PUT for the deploy
# wizard so any admin PC sees the same fleet picture. Added by
# update-backend (was missing in installs predating v0.12.0-rc2).
wizard:
  token_file: /etc/wg-monitor/wizard-token.txt
EOF`
	if _, err := s.MustRun(appendCmd); err != nil {
		return fmt.Errorf("append wizard block: %w", err)
	}
	PrintOK("backend.yaml: добавлен wizard: token_file")
	return nil
}

func actionUpdateBackend(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("В wizard.toml нет [backend] — сначала запусти install-backend")
		return fmt.Errorf("no backend configured")
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK(fmt.Sprintf("последний релиз: %s", rel.TagName))

	khPath := defaultCacheDir() + "/known_hosts"
	kh, err := NewKnownHosts(khPath)
	if err != nil {
		return err
	}

	PrintStep(1, 4, "SSH к VPS")
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()
	if err := stepCheckSSH(s, state.Backend.Host); err != nil {
		return err
	}

	PrintStep(2, 4, "Скачать бэкенд бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(3, 4, "Atomic upload + restart")
	// Ensure wizard token file + backend.yaml wizard: block exist BEFORE
	// the swap so the restarted backend picks up /v1/wizard/* endpoints on
	// first start. Idempotent — no-op when both already in place.
	if err := stepEnsureWizardSetup(s, secrets); err != nil {
		PrintWarn("wizard setup пропущен: " + err.Error())
	}
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", "wg-monitor-backend"); err != nil {
		return err
	}

	PrintStep(4, 4, "Verify /healthz")
	if state.Backend.Domain == "" {
		PrintWarn("домен не задан в wizard.toml — пропускаю /health проверку")
	} else {
		if err := stepVerifyBackendHealth(s, state.Backend.Domain); err != nil {
			return err
		}
		// Probe /v1/wizard/agents — expect 401 (endpoint registered + auth
		// required). 404 means the token file is unreadable by wgmonitor or
		// the wizard:-block in backend.yaml didn't parse — actionable hint.
		probeWizardEndpoint("https://" + state.Backend.Domain + "/v1/wizard/agents")
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}

// probeWizardEndpoint GETs /v1/wizard/agents with NO Authorization header.
// A correctly-wired backend returns 401 (endpoint registered, auth required).
// Anything else — 404 in particular — means the wizard.token_file in
// backend.yaml is empty/unreadable/missing, and the deploy wizard's [6] sync
// won't work. Best-effort: prints a colored line; never returns an error so
// it can't block the deploy flow.
func probeWizardEndpoint(url string) {
	cli := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return
	}
	resp, err := cli.Do(req)
	if err != nil {
		PrintWarn("wizard endpoint probe failed: " + err.Error())
		return
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		PrintOK("wizard endpoint: 401 (auth required, ready for [6] Sync)")
	case http.StatusNotFound:
		PrintWarn("wizard endpoint: 404 — /v1/wizard/* НЕ зарегистрирован")
		PrintInfo("  Возможные причины:")
		PrintInfo("  • /etc/wg-monitor/wizard-token.txt не читается процессом wgmonitor (проверь права)")
		PrintInfo("  • wizard:-блок в backend.yaml отсутствует или некорректен")
		PrintInfo("  • journalctl -u wg-monitor-backend -n 20 покажет точную причину")
	default:
		PrintWarn(fmt.Sprintf("wizard endpoint: HTTP %d (ожидался 401)", resp.StatusCode))
	}
}

func actionUpdateAgent(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	if len(state.Agents) == 0 {
		PrintFail("В wizard.toml нет [[agents]] — сначала install-agent / add-router")
		return fmt.Errorf("no agents configured")
	}
	var ag *AgentState
	if nickname != "" {
		ag = state.FindAgent(nickname)
		if ag == nil {
			PrintFail("агент с никнеймом " + nickname + " не найден в wizard.toml")
			return fmt.Errorf("agent not found")
		}
	} else if len(state.Agents) == 1 {
		ag = &state.Agents[0]
	} else {
		PrintWarn("несколько агентов — укажи --agent <nickname>")
		for _, a := range state.Agents {
			fmt.Println("  -", a.Nickname)
		}
		return fmt.Errorf("ambiguous agent")
	}

	// Update-flow только подменяет бинарь — init-скрипт, config.yaml, директории
	// и токен заводит actionInstallAgent. Если последний install ещё не
	// случался ([6] VPS Sync даёт запись с пустыми SSH-полями и пустой версией,
	// или сюда вручную добавили [[agents]] блок), update сломает роутер: стопнет
	// агента и попытается стартануть несуществующий S99-скрипт. Bail заранее,
	// до prompt'а пароля.
	if ag.LastDeployedVersion == "" {
		PrintFail(fmt.Sprintf(
			"%s ещё ни разу не устанавливался (last_deployed_version пуст в wizard.toml).\n"+
				"  Это update-flow, он только подменяет бинарь. Для первого install запусти [3] Добавить/установить роутер.",
			ag.Nickname))
		return fmt.Errorf("agent %s never deployed — use [3] install-agent first", ag.Nickname)
	}

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		// Fallback to global WG_KEENETIC_PASS
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	rep, cleanup, iface, perr := runPathDiscoveryStepWithNetfix(state, ag, secrets)
	defer cleanup()
	if perr != nil {
		PrintFail("path discovery: " + perr.Error())
		return perr
	}
	if rep.Chosen == nil {
		return diagnoseUnreachable(state, ag, rep, secrets)
	}
	if iface != "" && iface != ag.PreferredIface {
		ag.PreferredIface = iface
	}

	PrintStep(1, 4, fmt.Sprintf("SSH к роутеру %s (%s:%d)", ag.Nickname, ag.Host, portOrDefault(ag.Port, 222)))
	s, err := connectAgentSSHManaged(state, ag, pass, kh, "update-agent")
	if err != nil {
		// SSH-fail retry: оператор мог переехать роутером (новый IP / port
		// forward / ротированный пароль). Один re-prompt host/port/user/
		// password, ещё одна попытка, дальше — bail. Параллель к [3] install
		// (rc14+rc17), update-agent теперь тоже не молчит при auth/connect
		// fail вместо того чтобы тупо упасть.
		PrintWarn("SSH не подключился: " + err.Error())
		PrintInfo("введи параметры роутера руками (Enter — оставить текущее)")
		ag.Host = orDefault(Ask("Хост роутера", ag.Host), ag.Host)
		ag.Port = parseIntOr(Ask("SSH port", fmt.Sprint(portOrDefault(ag.Port, 222))), portOrDefault(ag.Port, 222))
		ag.User = orDefault(Ask("SSH user", userOrDefault(ag.User, "root")), userOrDefault(ag.User, "root"))
		if newPass := AskSecret("пароль root для " + ag.Nickname + " (Enter — оставить прежний)"); newPass != "" {
			pass = newPass
			if err := secrets.Set("WG_KEENETIC_PASS_"+strings.ToUpper(ag.Nickname), pass); err != nil && !errors.Is(err, ErrCacheDisabled) {
				PrintWarn("не смог обновить пароль в disk-кэше: " + err.Error())
			}
		}
		s, err = connectAgentSSHManaged(state, ag, pass, kh, "update-agent")
		if err != nil {
			return err
		}
	}
	defer s.Close()
	if err := stepCheckSSH(s, ag.Host); err != nil {
		return err
	}
	hostname := strings.TrimSpace(stepReadOrEmpty(s, "cat /proc/sys/kernel/hostname 2>/dev/null || uname -n"))
	mac := stepDetectPrimaryMAC(s)
	existingNick := stepReadExistingAgentNickname(s)

	// Defence-in-depth: state может врать (last_deployed_version выставлен
	// руками, или /opt был вайпнут после reset). Если init-скрипта нет, не
	// продолжаем — kill+swap+start с несуществующим S99 заведомо сломает
	// агента, оставив роутер молчащим.
	if _, _, rc, _ := s.Run("test -x /opt/etc/init.d/S99wg-monitor"); rc != 0 {
		PrintFail(fmt.Sprintf(
			"на %s нет /opt/etc/init.d/S99wg-monitor — агент не установлен на роутере, хотя в wizard.toml last_deployed_version=%q.\n"+
				"  Возможно, /opt был вайпнут или агент ставился без wizard'а. Запусти [3] Добавить/установить роутер.",
			ag.Nickname, ag.LastDeployedVersion))
		return fmt.Errorf("S99wg-monitor missing on %s — use [3] install-agent", ag.Nickname)
	}

	// MAC-pin guard: captured at first install (see actionInstallAgent step 2).
	// On mismatch we bail BEFORE any write — catches "operator switched SSTP
	// to the wrong tunnel" where the LAN-IP collides but the physical NIC
	// behind it is different. Bypassed (with warning) only when the pin is
	// unreadable — busybox without /sys/class/net.
	if err := verifyExpectedMAC(s, ag.ExpectedMAC); err != nil {
		PrintFail(err.Error())
		return err
	}
	if !confirmAmbiguousTarget(state, ag, hostname, mac, existingNick, "update-agent", Ask) {
		return fmt.Errorf("ambiguous target confirmation failed")
	}

	// Нормализация SSH-полей после успешного подключения. До этой точки оператор
	// мог иметь ag.User=="" / ag.Port==0 (например, агент попал в state через
	// [6] VPS Sync, где remote SSHUser=NULL и MergeAgents оставляет local пустым).
	// SSH мы устанавливали через `userOrDefault(ag.User, "root")` — фактически
	// "root", но это значение не возвращалось обратно в state. Дальше
	// pushToVPSBestEffort шлёт ag.* в PUT /v1/wizard/agents/<nick>, бэкенд
	// валидирует ssh_user/ssh_host/ssh_port/arch != "" и режет с HTTP 400.
	// Симметрично к ag.Arch ниже.
	if ag.User == "" {
		ag.User = "root"
	}
	if ag.Port == 0 {
		ag.Port = 222
	}

	PrintStep(2, 4, "Определить архитектуру")
	arch, err := stepDetectKeeneticArch(s)
	if err != nil {
		return err
	}
	if ag.Arch == "" {
		ag.Arch = arch
	}

	PrintStep(3, 4, "Скачать агент бинарь")
	assetName := "wg-monitor-agent-linux-" + arch
	localPath, err := stepDownloadAsset(dl, rel, assetName)
	if err != nil {
		return err
	}

	PrintStep(4, 4, "Stop → upload → swap → start")
	if err := stepUploadAgentBinary(s, localPath, "/opt/bin/wg-monitor"); err != nil {
		return err
	}

	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
	pushToVPSBestEffort(state, secrets, *ag)
	return nil
}

func portOrDefault(p, def int) int {
	if p == 0 {
		return def
	}
	return p
}

func userOrDefault(u, def string) string {
	if u == "" {
		return def
	}
	return u
}

// coldInstallIdentityGate is the Layer-2 cold-install confirm: prompt operator
// to confirm the physical box matches the nickname they're about to
// install under. Bypassed when:
//   - ExpectedMAC already pinned (re-install or update — verifyExpectedMAC
//     and Layer-1 path-discovery already covered identity)
//   - WG_YES_TO_ALL=1 (scripted runs)
//
// Returns true to proceed with install, false to bail. The `ask` callback
// is the prompt function — injected for test isolation; production uses Ask.
func coldInstallIdentityGate(ag *AgentState, hostname, mac, arch string, ask func(prompt, def string) string) bool {
	if ag.ExpectedMAC != "" {
		return true
	}
	if os.Getenv("WG_YES_TO_ALL") == "1" {
		return true
	}
	msg := fmt.Sprintf(
		"Это правильный роутер для install под nickname=%q? (hostname=%q mac=%s arch=%s) [y/N]",
		ag.Nickname, hostname, mac, arch,
	)
	ans := strings.ToLower(strings.TrimSpace(ask(msg, "")))
	return ans == "y" || ans == "yes" || ans == "д" || ans == "да"
}

func confirmAmbiguousTarget(state *State, ag *AgentState, hostname, mac, existingNick, op string, ask func(prompt, def string) string) bool {
	peers := agentsWithSameTarget(state, ag)
	if len(peers) < 2 || os.Getenv("WG_YES_TO_ALL") == "1" {
		return true
	}

	target := agentTargetKey(*ag)
	PrintWarn(fmt.Sprintf("double-check: target %s общий для нескольких агентов: %s", target, strings.Join(peers, ", ")))
	PrintInfo(fmt.Sprintf("подключились как %s к hostname=%q mac=%s existing_agent=%q", ag.Nickname, emptyAsQuestion(hostname), emptyAsQuestion(mac), existingNick))
	if ag.ExpectedMAC != "" {
		PrintInfo("ожидаемый pinned MAC для " + ag.Nickname + ": " + ag.ExpectedMAC)
	} else {
		PrintWarn("у " + ag.Nickname + " ещё нет expected_mac в wizard.toml — нужен ручной double-check")
	}
	answer := strings.TrimSpace(ask("Чтобы продолжить "+op+" для "+ag.Nickname+", введи nickname", ""))
	if answer != ag.Nickname {
		PrintFail("double-check не пройден: введено " + strconv.Quote(answer) + ", ожидалось " + strconv.Quote(ag.Nickname))
		return false
	}
	PrintOK("double-check: подтверждён " + ag.Nickname)
	return true
}

func connectAgentSSHManaged(state *State, ag *AgentState, pass string, kh *KnownHosts, op string) (*SSH, error) {
	port := portOrDefault(ag.Port, 222)
	user := userOrDefault(ag.User, "root")
	s, err := ConnectSSH(ag.Host, port, user, pass, kh, ag.Nickname)
	if !isHostKeyChangedErr(err) {
		return s, err
	}

	PrintWarn("SSH host key для " + ag.Nickname + " изменился. Запускаю managed double-check вместо ручного known-hosts forget.")
	probe, pub, probeErr := ConnectSSHInsecureCaptureKey(ag.Host, port, user, pass)
	if probeErr != nil {
		return nil, fmt.Errorf("host-key double-check probe failed: %w", probeErr)
	}
	defer probe.Close()

	hostname := strings.TrimSpace(stepReadOrEmpty(probe, "cat /proc/sys/kernel/hostname 2>/dev/null || uname -n"))
	mac := stepDetectPrimaryMAC(probe)
	existingNick := stepReadExistingAgentNickname(probe)
	PrintInfo(fmt.Sprintf("double-check probe: hostname=%q mac=%s existing_agent=%q", emptyAsQuestion(hostname), emptyAsQuestion(mac), existingNick))

	if ag.ExpectedMAC != "" {
		actual := extractMAC(mac)
		want := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(ag.ExpectedMAC, ":", ""), "-", ""))
		if actual == "" {
			return nil, fmt.Errorf("host key changed and expected_mac is pinned, but router MAC is unreadable")
		}
		if actual != want {
			return nil, fmt.Errorf("host key changed and MAC mismatch: got %s, expected %s", actual, want)
		}
		PrintOK("host-key double-check: MAC совпадает с pinned")
	}
	if existingNick != "" && existingNick != ag.Nickname {
		return nil, fmt.Errorf("host key changed, but router already hosts agent %q, expected %q", existingNick, ag.Nickname)
	}
	if !confirmAmbiguousTarget(state, ag, hostname, mac, existingNick, op+" host-key rotation", Ask) {
		return nil, fmt.Errorf("host-key rotation cancelled")
	}
	if ag.ExpectedMAC == "" && existingNick == "" {
		PrintWarn("нет ни expected_mac, ни existing agent.nickname — принимаю новый host key только по ручному double-check")
	}
	if err := kh.ReplaceHostKey(ag.Nickname, pub); err != nil {
		return nil, fmt.Errorf("replace known_hosts for %s: %w", ag.Nickname, err)
	}
	PrintOK("known_hosts обновлён для " + ag.Nickname)
	return ConnectSSH(ag.Host, port, user, pass, kh, ag.Nickname)
}

func agentsWithSameTarget(state *State, ag *AgentState) []string {
	if state == nil || ag == nil {
		return nil
	}
	key := agentTargetKey(*ag)
	var names []string
	for _, other := range state.Agents {
		if agentTargetKey(other) == key {
			names = append(names, other.Nickname)
		}
	}
	sort.Strings(names)
	return names
}

func agentTargetKey(ag AgentState) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(ag.Host), portOrDefault(ag.Port, 222))
}

func emptyAsQuestion(s string) string {
	if strings.TrimSpace(s) == "" {
		return "?"
	}
	return s
}

func actionInstallAgent(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	if os.Getenv("WG_LEGACY_ROUTER_SSH") == "1" {
		return actionInstallAgentLegacySSH(state, secrets, dl, nickname)
	}
	return actionInstallAgentAWGM(state, secrets, dl, nickname)
}

func actionInstallAgentAWGM(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	if state.Backend.Domain == "" {
		return fmt.Errorf("backend domain is not configured; run install-backend first")
	}
	if dl == nil {
		dl = NewDownloader()
	}
	var ag *AgentState
	if nickname != "" {
		ag = state.FindAgent(nickname)
	}
	if ag == nil {
		nick := nickname
		if nick == "" {
			nick = Ask("Никнейм роутера (a-z0-9_-, 2-16)", "")
		}
		if nick == "" {
			return fmt.Errorf("nickname required")
		}
		state.Agents = append(state.Agents, AgentState{Nickname: nick})
		ag = &state.Agents[len(state.Agents)-1]
	}
	if ag.Kind == "" {
		ag.Kind = "static"
	}
	if ag.AWGMURL == "" {
		ag.AWGMURL = normalizeAWGMURL(Ask("AWG Manager URL (KeenDNS, https://...)", ag.AWGMURL))
	} else {
		ag.AWGMURL = normalizeAWGMURL(ag.AWGMURL)
	}
	if ag.AWGMURL == "" {
		return fmt.Errorf("AWG Manager URL required")
	}
	ag.DeployMode = "awgm"
	if ag.AWGMAuth == "" {
		ag.AWGMAuth = "router-admin"
	}

	wizardToken := secrets.GetNonInteractive("WIZARD_TOKEN")
	if wizardToken == "" {
		wizardToken, _ = secrets.Get("WIZARD_TOKEN", "wizard API token (из /etc/wg-monitor/wizard-token.txt на VPS)", nil)
	}
	if wizardToken == "" {
		return fmt.Errorf("WIZARD_TOKEN required for AWG Manager enrollment")
	}
	vps := NewVPSClientForBackend(state, wizardToken, 15*time.Second)
	if vps == nil {
		return fmt.Errorf("wizard API client is not configured")
	}

	PrintStep(1, 5, "VPS: создать/обновить enrollment "+ag.Nickname)
	enroll, err := vps.CreateEnrollment(context.Background(), EnrollmentRequest{
		Nickname: ag.Nickname,
		Kind:     ag.Kind,
		ThreadID: int64(ag.ThreadID),
	})
	if err != nil {
		return err
	}
	tokenEnv := agentTokenEnv(ag.Nickname)
	if err := secrets.Set(tokenEnv, enroll.RawToken); err != nil {
		if errors.Is(err, ErrCacheDisabled) {
			PrintWarn("WG_NO_SECRET_CACHE=1 — raw-token не сохранён на диск. Сохрани вручную:")
			fmt.Printf("    %s=%s\n", tokenEnv, enroll.RawToken)
		} else {
			return fmt.Errorf("save %s: %w", tokenEnv, err)
		}
	}
	_ = os.Setenv(tokenEnv, enroll.RawToken)

	apiKey, login, pass := awgmAuthForAgent(secrets, ag.Nickname)
	terminalUser := userOrDefault(ag.User, "root")
	terminalPass := routerRootPasswordForAgent(secrets, ag.Nickname)

	PrintStep(2, 5, "AWG Manager: auth + system info")
	awgm := NewAWGMClient(ag.AWGMURL, login, pass).WithAPIKey(apiKey)
	if err := awgm.Login(context.Background()); err != nil {
		return err
	}
	info, err := awgm.SystemInfo(context.Background())
	awgmAuthMode := "router-admin"
	if apiKey != "" {
		awgmAuthMode = "api-key"
	}
	if err != nil && apiKey != "" && isAWGMUnauthorized(err) {
		PrintWarn("AWG Manager API key получил 401 от web layer; fallback на login/password")
		apiKey = ""
		login, pass = awgmLoginPasswordForAgent(secrets, ag.Nickname)
		awgm = NewAWGMClient(ag.AWGMURL, login, pass)
		if err := awgm.Login(context.Background()); err != nil {
			return err
		}
		info, err = awgm.SystemInfo(context.Background())
		awgmAuthMode = "router-admin"
	}
	if err != nil {
		return err
	}
	if ag.Arch == "" && info.GoArch != "" {
		ag.Arch = info.GoArch
	}
	if ag.Arch == "" {
		return fmt.Errorf("AWG Manager did not report goArch; set arch in wizard.toml")
	}

	PrintStep(3, 5, "GitHub release + bootstrap script")
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}
	assetName := "wg-monitor-agent-linux-" + ag.Arch
	asset := rel.AssetByName(assetName)
	if asset == nil {
		return fmt.Errorf("release %s has no asset %s", rel.TagName, assetName)
	}
	sums := rel.AssetByName("checksums.txt")
	if sums == nil {
		return fmt.Errorf("release %s has no checksums.txt", rel.TagName)
	}
	script, err := RenderAWGMBootstrapScript(AWGMBootstrapParams{
		Nickname:     ag.Nickname,
		BackendURL:   enroll.BackendURL,
		RawToken:     enroll.RawToken,
		Version:      rel.TagName,
		DownloadURL:  releaseAssetURLForRouter(state, rel.TagName, assetName, asset.DownloadURL),
		ChecksumURL:  releaseAssetURLForRouter(state, rel.TagName, "checksums.txt", sums.DownloadURL),
		ChecksumName: assetName,
	})
	if err != nil {
		return err
	}

	PrintStep(4, 5, "AWG Manager terminal: bootstrap Entware")
	var res TerminalRunResult
	if shouldRunAWGMBootstrapViaVPS(state, ag.AWGMURL) {
		PrintInfo("public AWG Manager URL detected - running terminal bootstrap from VPS")
		res, err = runAWGMBootstrapViaVPS(state, secrets, ag, apiKey, login, pass, terminalUser, terminalPass, script)
	} else {
		res, err = runAWGMBootstrapDirect(awgm, script, terminalUser, terminalPass)
	}
	if err != nil {
		if strings.TrimSpace(res.Output) != "" {
			PrintInfo(res.Output)
		}
		return err
	}

	PrintStep(5, 5, "Сохранить локальное состояние")
	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
	if ag.Host == "" && info.RouterIP != "" {
		ag.Host = info.RouterIP
	}
	ag.AWGMAuth = awgmAuthMode
	PrintOK("агент установлен через AWG Manager/KeenDNS: " + ag.Nickname)
	return nil
}

func awgmAuthForAgent(secrets *SecretStore, nickname string) (apiKey, login, password string) {
	suffix := strings.ToUpper(nickname)
	apiKey, _ = secrets.Get("WG_AWGM_API_KEY_"+suffix, "AWG Manager API key for "+nickname+" (Enter to use login/password)", nil)
	if apiKey != "" {
		return apiKey, "", ""
	}
	login, password = awgmLoginPasswordForAgent(secrets, nickname)
	return "", login, password
}

func awgmLoginPasswordForAgent(secrets *SecretStore, nickname string) (login, password string) {
	suffix := strings.ToUpper(nickname)
	login, _ = secrets.Get("WG_AWGM_LOGIN_"+suffix, "AWG Manager login for "+nickname, nil)
	password, _ = secrets.Get("WG_AWGM_PASS_"+suffix, "AWG Manager password for "+nickname, nil)
	return login, password
}

func isAWGMUnauthorized(err error) bool {
	var httpErr *AWGMHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized
}

func routerRootPasswordForAgent(secrets *SecretStore, nickname string) string {
	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "Entware terminal root password for "+nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "Entware terminal root password", nil)
	}
	return pass
}

func actionInstallAgentLegacySSH(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}

	// 1. Найти или создать агента
	var ag *AgentState
	if nickname != "" {
		ag = state.FindAgent(nickname)
	}
	if ag == nil {
		// Создать новую запись
		nick := nickname
		if nick == "" {
			nick = Ask("Никнейм роутера (a-z0-9_-, 2-16)", "")
		}
		if nick == "" {
			return fmt.Errorf("nickname required")
		}
		// Повторный FindAgent после prompt'а: если оператор только что
		// ввёл nickname агента, добавленного через [3] — переиспользуем
		// существующий entry вместо создания дубликата в state.Agents.
		ag = state.FindAgent(nick)
		if ag == nil {
			state.Agents = append(state.Agents, AgentState{Nickname: nick})
			ag = &state.Agents[len(state.Agents)-1]
		}
	}

	// Каждый Ask условный — поле уже могли заполнить в state (через
	// actionAddRouter, или вручную в wizard.toml, или прошлый прогон).
	// Цель — нулевые prompt'ы на повторных запусках и при вызове из [3].
	if ag.Host == "" {
		ag.Host = orDefault(Ask("Хост роутера", "192.168.31.1"), "192.168.31.1")
	}
	if ag.Port == 0 {
		ag.Port = parseIntOr(Ask("SSH port", "222"), 222)
	}
	if ag.User == "" {
		ag.User = orDefault(Ask("SSH user", "root"), "root")
	}
	// thread_id оставляем 0, если не задан: backend сам выставит
	// telegram_thread_id в users-таблице на первом hard-alert от агента.

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	tokenEnv := "WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname)
	// env (выставляет [3] в текущем процессе) + disk-кэш (выставляет [3]
	// между сессиями). Случайно сгенерить новый токен здесь нельзя —
	// раскорреляция с DB (token_hash на VPS не совпадёт) → backend
	// будет отвергать heartbeat'ы агента молча.
	tok := secrets.GetNonInteractive(tokenEnv)
	if tok == "" {
		PrintFail(fmt.Sprintf(
			"токен %s не найден ни в env, ни в disk-кэше.\n"+
				"  Это значит, что %s ещё не зарегистрирован в DB на VPS.\n"+
				"  Запусти [3] Добавить/установить роутер — wizard сам создаст user'а через wg-monitor-cli\n"+
				"  и сохранит raw-токен в disk-кэш. После этого можно использовать install-agent --agent "+ag.Nickname+" для переустановки.",
			tokenEnv, ag.Nickname))
		return fmt.Errorf("agent token for %s not in cache; use [3] add/install router first", ag.Nickname)
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	rep2, cleanup2, iface2, perr2 := runPathDiscoveryStepWithNetfix(state, ag, secrets)
	defer cleanup2()
	if perr2 != nil {
		PrintFail("path discovery: " + perr2.Error())
		return perr2
	}
	if rep2.Chosen == nil {
		return diagnoseUnreachable(state, ag, rep2, secrets)
	}
	if iface2 != "" && iface2 != ag.PreferredIface {
		ag.PreferredIface = iface2
	}

	PrintStep(1, 8, fmt.Sprintf("SSH к роутеру %s:%d (%s)", ag.Host, ag.Port, ag.User))
	s, err := connectAgentSSHManaged(state, ag, pass, kh, "install-agent")
	if err != nil {
		// Single retry: дефолты (192.168.31.1:222 root) подходят к
		// Keenetic из коробки, но если у оператора другой роутер /
		// нестандартный port forward / user не root / устаревший
		// disk-кэш с не тем паролем — даём перевввести и host/port/
		// user, и пароль (Enter — оставить текущий). Дальше — bail.
		PrintWarn("SSH не подключился: " + err.Error())
		PrintInfo("введи параметры роутера руками (Enter — оставить текущее)")
		ag.Host = orDefault(Ask("Хост роутера", ag.Host), ag.Host)
		ag.Port = parseIntOr(Ask("SSH port", fmt.Sprint(ag.Port)), ag.Port)
		ag.User = orDefault(Ask("SSH user", ag.User), ag.User)
		if newPass := AskSecret("пароль root для " + ag.Nickname + " (Enter — оставить прежний)"); newPass != "" {
			pass = newPass
			// Перезаписываем disk-кэш под per-nickname env — иначе
			// при следующем запуске опять подтянется старый
			// (кривой) пароль и retry-цикл повторится.
			if err := secrets.Set("WG_KEENETIC_PASS_"+strings.ToUpper(ag.Nickname), pass); err != nil && !errors.Is(err, ErrCacheDisabled) {
				PrintWarn("не смог обновить пароль в disk-кэше: " + err.Error())
			}
		}
		s, err = connectAgentSSHManaged(state, ag, pass, kh, "install-agent")
		if err != nil {
			return err
		}
	}
	defer s.Close()

	PrintStep(2, 8, "Идентификация роутера")
	hostname := strings.TrimSpace(stepReadOrEmpty(s, "cat /proc/sys/kernel/hostname 2>/dev/null || uname -n"))
	mac := stepDetectPrimaryMAC(s)
	existingNick := stepReadExistingAgentNickname(s)

	// MAC-pin guard for re-install: if state already has an expected_mac
	// for this nickname (someone ran install once before), refuse to
	// overwrite when the connected box's MAC differs — same physical
	// identity protection as actionUpdateAgent. Bypass on first install
	// (ExpectedMAC == "") and on unreadable MAC (busybox quirk).
	if err := verifyExpectedMAC(s, ag.ExpectedMAC); err != nil {
		PrintFail(err.Error())
		return err
	}

	// Всегда печатаем — оператор видит к чему реально подключились,
	// даже если все поля пустые (тогда видна аномалия).
	if hostname == "" {
		hostname = "?"
	}
	if mac == "" {
		mac = "?"
	}
	PrintInfo(fmt.Sprintf("hostname=%q  mac=%s", hostname, mac))

	if !coldInstallIdentityGate(ag, hostname, mac, "?", Ask) {
		PrintFail("install отменён оператором")
		return fmt.Errorf("install cancelled — identity not confirmed")
	}
	if !confirmAmbiguousTarget(state, ag, hostname, mac, existingNick, "install-agent", Ask) {
		return fmt.Errorf("ambiguous target confirmation failed")
	}

	// Capture normalised MAC for future verifyExpectedMAC calls. On first
	// install this is the moment we declare "this physical NIC = this
	// nickname"; subsequent SSH-write ops cross-check against it. Empty
	// readback (mac == "?") leaves the pin unset and re-pins on next run.
	if normalised := extractMAC(mac); normalised != "" {
		ag.ExpectedMAC = normalised
	}

	// Layer-4 inline-suggest: if chosen path already has our nickname installed
	// AND there were other responding candidates from Layer 1, probe them too
	// to catch double-deploy ("operator installed on local box by mistake, then
	// installed on remote box too"). Cheap path: skip when chosen path is the
	// only responder, OR when existingNick is empty (clean box).
	if existingNick != "" && rep2.Multiple {
		nicks := map[string]string{rep2.Chosen.Iface: existingNick}
		for _, c := range rep2.Candidates {
			if c.Iface == rep2.Chosen.Iface || !c.Responded() {
				continue
			}
			tok, addErr := NewRealProber().AddRoute(ag.Host, c.Index)
			if addErr != nil {
				continue
			}
			otherSSH, sshErr := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
			if sshErr == nil {
				nicks[c.Iface] = stepReadExistingAgentNickname(otherSSH)
				otherSSH.Close()
			}
			_ = NewRealProber().DelRoute(tok)
		}
		if detectDoubleDeploy(rep2, nicks, ag.Nickname) {
			PrintWarn(fmt.Sprintf("⚠ агент %q стоит на ДВУХ роутерах одновременно — это ошибочный двойной деплой", ag.Nickname))
			for iface, nick := range nicks {
				if nick == ag.Nickname {
					PrintWarn("  • " + iface)
				}
			}
			ans := strings.ToLower(strings.TrimSpace(Ask(
				"снять с локального (не-выбранного) пути и продолжить install на выбранном? [y/N]", "")))
			if ans == "y" || ans == "yes" || ans == "д" || ans == "да" {
				for _, c := range rep2.Candidates {
					if c.Iface == rep2.Chosen.Iface {
						continue
					}
					if nicks[c.Iface] == ag.Nickname {
						PrintInfo("снимаю агента через " + c.Iface)
						tok, _ := NewRealProber().AddRoute(ag.Host, c.Index)
						if err := actionUninstallAgent(state, secrets, UninstallTarget{
							Nickname: ag.Nickname, Host: ag.Host, Port: ag.Port, User: ag.User,
						}); err != nil {
							PrintWarn("uninstall на " + c.Iface + ": " + err.Error())
						}
						_ = NewRealProber().DelRoute(tok)
					}
				}
			}
		}
	}

	switch {
	case existingNick == "":
		PrintInfo("существующий агент: (нет, чистый роутер)")
	case existingNick == ag.Nickname:
		PrintInfo(fmt.Sprintf("существующий агент: %q — переустановка", existingNick))
	default:
		PrintFail(fmt.Sprintf(
			"на роутере уже установлен агент под именем %q — НЕ перезаписываю под %q. "+
				"Возможные причины: (a) ты случайно цепляешься не к тому роутеру (VPN не активен?); "+
				"(b) хочешь честно переименовать — тогда сначала убери его на VPS "+
				"(sqlite3 /var/lib/wg-monitor/state.db \"DELETE FROM users WHERE nickname='%s';\") "+
				"и удали /opt/etc/wg-monitor/config.yaml на роутере.",
			existingNick, ag.Nickname, existingNick))
		return fmt.Errorf("router already hosts agent %q, refusing to overwrite", existingNick)
	}

	PrintStep(3, 8, "Архитектура")
	arch, err := stepDetectKeeneticArch(s)
	if err != nil {
		return err
	}
	ag.Arch = arch

	PrintStep(4, 8, "Директории /opt/{bin,etc/wg-monitor,etc/init.d,var/wg-monitor}")
	if _, err := s.MustRun("mkdir -p /opt/bin /opt/etc/wg-monitor /opt/etc/init.d /opt/var/wg-monitor"); err != nil {
		return err
	}
	PrintOK("ok")

	PrintStep(5, 8, "config.yaml")
	cfg, err := RenderAgentYAML(AgentParams{
		BackendURL: "https://" + state.Backend.Domain,
		Token:      tok,
		Nickname:   ag.Nickname,
	})
	if err != nil {
		return err
	}
	// dropbear: prefer UploadStdin
	if err := s.UploadStdin("/opt/etc/wg-monitor/config.yaml", cfg); err != nil {
		return err
	}
	s.MustRun("chmod 600 /opt/etc/wg-monitor/config.yaml")
	PrintOK("/opt/etc/wg-monitor/config.yaml")

	PrintStep(6, 8, "init.d скрипт")
	initd, err := ReadStaticTemplate("S99wg-monitor")
	if err != nil {
		return err
	}
	if err := s.UploadStdin("/opt/etc/init.d/S99wg-monitor", initd); err != nil {
		return err
	}
	s.MustRun("chmod +x /opt/etc/init.d/S99wg-monitor")
	PrintOK("/opt/etc/init.d/S99wg-monitor")

	PrintStep(7, 8, "Скачать агент бинарь")
	assetName := "wg-monitor-agent-linux-" + arch
	localPath, err := stepDownloadAsset(dl, rel, assetName)
	if err != nil {
		return err
	}

	PrintStep(8, 8, "Upload + start")
	if err := stepUploadAgentBinary(s, localPath, "/opt/bin/wg-monitor"); err != nil {
		return err
	}

	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
	// Best-effort sync push so other wizard PCs see this deploy.
	if a := state.FindAgent(ag.Nickname); a != nil {
		pushToVPSBestEffort(state, secrets, *a)
	}
	return nil
}

func actionInstallBackend(state *State, secrets *SecretStore, dl *Downloader) error {
	previousBackend := state.Backend
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	// 1. Запросить параметры
	state.Backend.Host = Ask("VPS host или IP", state.Backend.Host)
	state.Backend.Port = parseIntOr(Ask("SSH port", strOrDefault(state.Backend.Port, "22")), 22)
	state.Backend.User = orDefault(Ask("SSH user", strOrDefaultS(state.Backend.User, "root")), "root")
	configureBackendSSHAuth(state)
	state.Backend.Domain = cleanPromptDefaultLeak(Ask("Домен бэкенда (например wgmon.example.com)", state.Backend.Domain))
	caddyEmail := cleanPromptDefaultLeak(Ask("Email для Let's Encrypt", "admin@"+state.Backend.Domain))

	if state.Telegram.ChatID == 0 {
		state.Telegram.ChatID = parseInt64Or(Ask("Telegram chat_id (отрицательное число)", ""), 0)
	}
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = parseInt64Or(Ask("Telegram admin user_id (твой User ID)", ""), 0)
	}

	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token (1234:ABC...)", nil)

	if botToken == "" || state.Backend.Host == "" || state.Backend.Domain == "" {
		PrintFail("обязательные поля пустые")
		return fmt.Errorf("missing required fields")
	}

	// 2. SSH
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 14, "SSH к VPS")
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()

	// Pre-flight: detect existing install and require explicit confirmation.
	// install-backend перезаписывает backend.yaml, bot-token.txt и unit-файл
	// — на работающем backend'е без подтверждения это может молча сломать
	// прод (другой chat_id/admin_user_id, устаревший токен из disk-cache, и т.п.).
	existingInstall := existingInstallDetected(s)
	if existingInstall {
		existingChat, existingAdmin := readDeployedTelegramMeta(s)
		PrintWarn("на VPS уже установлен wg-monitor-backend:")
		PrintInfo(fmt.Sprintf("  существующий chat_id=%d admin_user_id=%d", existingChat, existingAdmin))
		PrintInfo(fmt.Sprintf("  будет записан chat_id=%d admin_user_id=%d", state.Telegram.ChatID, state.Telegram.AdminUserID))
		ans := strings.ToLower(strings.TrimSpace(Ask("перезаписать backend.yaml + bot-token.txt + unit? [y/N]", "n")))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("install-backend aborted by user (existing install detected)")
		}
	}

	PrintStep(2, 14, "User wgmonitor")
	if err := stepEnsureUser(s, "wgmonitor"); err != nil {
		return err
	}

	PrintStep(3, 14, "Директории")
	stepEnsureDir(s, "/etc/wg-monitor", "")
	stepEnsureDir(s, "/var/lib/wg-monitor", "wgmonitor:wgmonitor")

	PrintStep(4, 14, "backend.yaml")
	yamlBytes, err := RenderBackendYAML(BackendParams{
		ChatID:      state.Telegram.ChatID,
		AdminUserID: state.Telegram.AdminUserID,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/wg-monitor/backend.yaml", yamlBytes, "640"); err != nil {
		return err
	}
	if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/backend.yaml"); err != nil {
		PrintWarn("chown backend.yaml: " + err.Error())
	}

	PrintStep(5, 14, "bot-token.txt")
	// Отдельный файл, потому что backend.yaml читаем сервисом wgmonitor,
	// а bot-token.txt не попадает в YAML и доступен только root + wgmonitor.
	// Drift detection: если файл уже есть и его содержимое != локальному кэшу,
	// предупреждаем перед перезаписью. Помогает поймать сценарий "оператор
	// ротировал токен на VPS вручную, локальный кэш устарел".
	newBot := strings.TrimSpace(botToken) + "\n"
	if existingBot, ok := readRemoteFile(s, "/etc/wg-monitor/bot-token.txt"); ok {
		if strings.TrimSpace(existingBot) != strings.TrimSpace(botToken) {
			PrintWarn("bot-token.txt на VPS отличается от локального WG_BOT_TOKEN — перезаписываю значением из локального кэша")
			PrintInfo("  если на VPS правильный (а локальный устарел), отмени и обнови WG_BOT_TOKEN в " + secretsCachePath())
			ans := strings.ToLower(strings.TrimSpace(Ask("перезаписать удалённый bot-token? [y/N]", "n")))
			if ans != "y" && ans != "yes" {
				return fmt.Errorf("bot-token overwrite aborted by user")
			}
		}
	}
	if err := stepUploadFile(s, "/etc/wg-monitor/bot-token.txt", []byte(newBot), "640"); err != nil {
		return err
	}
	if _, err := s.MustRun("chown root:wgmonitor /etc/wg-monitor/bot-token.txt"); err != nil {
		PrintWarn("chown bot-token.txt: " + err.Error())
	}

	PrintStep(6, 14, "wizard-token.txt")
	// Idempotent helper handles both upload (mode 640 root:wgmonitor) and
	// the backend.yaml wizard:-block. The template already includes the
	// block, so the probe inside the helper will skip the append step.
	if err := stepEnsureWizardSetup(s, secrets); err != nil {
		return fmt.Errorf("wizard setup: %w", err)
	}

	PrintStep(7, 14, "systemd unit")
	unit, err := ReadStaticTemplate("wg-monitor-backend.service")
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/systemd/system/wg-monitor-backend.service", unit, "644"); err != nil {
		return err
	}
	if _, err := s.MustRun("systemctl daemon-reload && systemctl enable wg-monitor-backend"); err != nil {
		return err
	}
	PrintOK("daemon-reload + enable")

	PrintStep(8, 14, "Caddy")
	if err := stepInstallCaddy(s); err != nil {
		return err
	}

	PrintStep(9, 14, "Caddyfile")
	cf, err := RenderCaddyfile(CaddyParams{Domain: state.Backend.Domain, Email: caddyEmail})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/caddy/Caddyfile", cf, "644"); err != nil {
		return err
	}
	if _, err := s.MustRun("systemctl enable --now caddy && systemctl reload caddy"); err != nil {
		PrintWarn("caddy reload не прошёл — возможно, не установлен")
	} else {
		PrintOK("caddy reloaded")
	}

	PrintStep(10, 14, "Скачать backend бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(11, 14, "Upload + sha + swap")
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", backendInstallSwapService(existingInstall)); err != nil {
		return err
	}

	PrintStep(12, 14, "Start service")
	if _, err := s.MustRun("systemctl start wg-monitor-backend"); err != nil {
		return err
	}
	PrintOK("wg-monitor-backend started")

	time.Sleep(3 * time.Second)

	PrintStep(13, 14, "Verify systemctl is-active")
	out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
	if strings.TrimSpace(out) != "active" {
		PrintFail("сервис не active. Логи:")
		jr, _ := s.MustRun("journalctl -u wg-monitor-backend -n 30 --no-pager")
		fmt.Println(jr)
		return fmt.Errorf("service not active")
	}
	PrintOK("active")

	PrintStep(14, 14, "Verify /health через домен")
	if err := stepVerifyBackendHealth(s, state.Backend.Domain); err != nil {
		PrintWarn("health check не прошёл — возможно DNS ещё не прогрелся, проверь руками")
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	if shouldOfferBackendMigration(previousBackend, state.Backend, len(state.Agents)) {
		PrintWarn(fmt.Sprintf(
			"Похоже, backend переехал: %s/%s -> %s/%s",
			emptyAsQuestion(previousBackend.Host), emptyAsQuestion(previousBackend.Domain),
			emptyAsQuestion(state.Backend.Host), emptyAsQuestion(state.Backend.Domain),
		))
		PrintInfo("Миграция восстановит users DB на новом VPS из локальных WG_AGENT_TOKEN_<NICK> и перепишет config.yaml на роутерах.")
		ans := strings.ToLower(strings.TrimSpace(Ask("Перенести существующих агентов на этот VPS сейчас? [y/N]", "n")))
		if ans == "y" || ans == "yes" || ans == "д" || ans == "да" {
			if err := ensureRemoteSQLite3(s); err != nil {
				return fmt.Errorf("prepare migration: %w", err)
			}
			if err := migrateBackendAgents(state, secrets, s, ""); err != nil {
				return fmt.Errorf("post-install backend migration: %w", err)
			}
		}
	}
	return nil
}

func actionMigrateBackend(state *State, secrets *SecretStore, nickname string) error {
	if state.Backend.Host == "" || state.Backend.Domain == "" {
		return fmt.Errorf("backend is not configured; run install-backend first")
	}
	if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
		return migrateBackendAgents(state, secrets, nil, nickname)
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 1, "SSH к новому VPS")
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer bs.Close()
	if err := ensureRemoteSQLite3(bs); err != nil {
		return err
	}
	return migrateBackendAgents(state, secrets, bs, nickname)
}

func shouldOfferBackendMigration(oldBackend, newBackend BackendState, agentCount int) bool {
	if agentCount == 0 {
		return false
	}
	oldHasBackend := strings.TrimSpace(oldBackend.Host) != "" || strings.TrimSpace(oldBackend.Domain) != ""
	if !oldHasBackend {
		return false
	}
	newHasBackend := strings.TrimSpace(newBackend.Host) != "" && strings.TrimSpace(newBackend.Domain) != ""
	if !newHasBackend {
		return false
	}
	return strings.TrimSpace(oldBackend.Host) != strings.TrimSpace(newBackend.Host) ||
		strings.TrimSpace(oldBackend.Domain) != strings.TrimSpace(newBackend.Domain)
}

func actionAddRouter(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("сначала install-backend (нужно куда добавлять)")
		return fmt.Errorf("no backend")
	}
	nick := Ask("Никнейм нового роутера (a-z, 2-16)", "")
	if nick == "" {
		return fmt.Errorf("nickname required")
	}
	if !cliNicknameRe.MatchString(nick) {
		PrintFail(fmt.Sprintf("nickname %q не подходит под ^[a-z][a-z0-9_-]{1,15}$ (требование wg-monitor-cli)", nick))
		return fmt.Errorf("invalid nickname")
	}

	ag := state.FindAgent(nick)
	if ag == nil {
		state.Agents = append(state.Agents, AgentState{Nickname: nick, Kind: "static"})
		ag = &state.Agents[len(state.Agents)-1]
	}
	kind := strings.ToLower(strings.TrimSpace(orDefault(Ask("Kind (static / mobile)", orDefault(ag.Kind, "static")), orDefault(ag.Kind, "static"))))
	if kind != "static" && kind != "mobile" {
		PrintFail("kind должен быть static или mobile")
		return fmt.Errorf("invalid kind")
	}
	ag.Kind = kind
	if ag.Kind == "" {
		ag.Kind = "static"
	}
	ag.DeployMode = "awgm"
	ag.AWGMAuth = "router-admin"
	ag.AWGMURL = normalizeAWGMURL(Ask("AWG Manager URL (KeenDNS, https://...)", ag.AWGMURL))
	if ag.AWGMURL == "" {
		return fmt.Errorf("AWG Manager URL required")
	}

	// Telegram-топик: если ag.ThreadID == 0, пытаемся создать через Bot API.
	// Если chat_id не задан или Bot API возвращает ошибку — оставляем 0;
	// топик создастся автоматически на первом hard-alert от агента.
	PrintStep(2, 3, "Telegram форум-топик")
	if ag.ThreadID == 0 {
		threadID := autoCreateForumTopic(state, secrets, nick)
		if threadID != 0 {
			ag.ThreadID = threadID
		} else {
			PrintInfo("thread_id=0 — топик создастся автоматически на первом hard-alert")
		}
	} else {
		PrintInfo(fmt.Sprintf("thread_id=%d уже сохранён, пропускаю createForumTopic", ag.ThreadID))
	}

	PrintStep(3, 3, "Установить агента на роутер")
	return actionInstallAgentAWGM(state, secrets, dl, nick)
}

func actionRepairAgentToken(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
	if state.Backend.Host == "" {
		return fmt.Errorf("no backend configured")
	}
	ag, err := chooseAgentForAction(state, nickname, "восстановления токена")
	if err != nil {
		return err
	}
	if !cliNicknameRe.MatchString(ag.Nickname) {
		return fmt.Errorf("nickname %q does not match %s", ag.Nickname, cliNicknameRe.String())
	}

	if os.Getenv("WG_YES_TO_ALL") != "1" {
		PrintWarn("Будет создан новый agent token. Старый токен перестанет подходить после обновления token_hash на VPS.")
		ans := strings.ToLower(strings.TrimSpace(Ask("продолжить восстановление токена для "+ag.Nickname+"? [y/N]", "n")))
		if ans != "y" && ans != "yes" && ans != "д" && ans != "да" {
			return fmt.Errorf("repair-agent-token cancelled")
		}
	}
	if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
		PrintInfo("repair-agent-token: re-enroll через AWG Manager/KeenDNS")
		return actionInstallAgentAWGM(state, secrets, dl, ag.Nickname)
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	PrintStep(1, 4, "VPS: проверить запись "+ag.Nickname+" в users DB")
	bs, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	if err := ensureRemoteSQLite3(bs); err != nil {
		bs.Close()
		return err
	}
	exists, err := vpsUserExists(bs, ag.Nickname)
	bs.Close()
	if err != nil {
		return fmt.Errorf("check users table: %w", err)
	}
	if !exists {
		return fmt.Errorf("agent %s is not in VPS users DB; run add-router first", ag.Nickname)
	}
	PrintOK("users DB: " + ag.Nickname + " найден")

	PrintStep(2, 4, "Сгенерировать новый raw-token")
	rawToken, err := generateAgentRawToken()
	if err != nil {
		return err
	}
	tokenEnv := "WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname)
	oldEnv, hadOldEnv := os.LookupEnv(tokenEnv)
	if err := stageAgentRawToken(secrets, tokenEnv, rawToken); err != nil {
		if errors.Is(err, ErrCacheDisabled) {
			PrintWarn("WG_NO_SECRET_CACHE=1 — staged raw-token не сохранён на диск. Сохрани вручную для recovery:")
			fmt.Printf("    %s=%s\n", tokenEnv, rawToken)
		} else {
			return fmt.Errorf("stage %s_STAGED: %w", tokenEnv, err)
		}
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if hadOldEnv {
			os.Setenv(tokenEnv, oldEnv)
		} else {
			os.Unsetenv(tokenEnv)
		}
	}()
	PrintOK(tokenEnv + " подготовлен в памяти процесса")

	PrintStep(3, 4, "Переустановить конфиг агента на роутере")
	if err := actionInstallAgent(state, secrets, dl, ag.Nickname); err != nil {
		return fmt.Errorf("install agent with new token: %w", err)
	}

	PrintStep(4, 4, "VPS: обновить token_hash и сохранить raw-token")
	bs, err = connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer bs.Close()
	if err := vpsUpdateAgentTokenHash(bs, ag.Nickname, hashAgentRawToken(rawToken)); err != nil {
		return err
	}
	if err := secrets.Set(tokenEnv, rawToken); err != nil {
		if errors.Is(err, ErrCacheDisabled) {
			PrintWarn("WG_NO_SECRET_CACHE=1 — raw-token не сохранён на диск. Сохрани вручную:")
			fmt.Printf("    %s=%s\n", tokenEnv, rawToken)
		} else {
			return fmt.Errorf("save %s: %w", tokenEnv, err)
		}
	}
	committed = true
	PrintOK("token_hash на VPS и " + tokenEnv + " в локальном кэше синхронизированы")
	return nil
}

func migrateBackendAgents(state *State, secrets *SecretStore, bs *SSH, nickname string) error {
	targets, err := migrationTargets(state, nickname)
	if err != nil {
		return err
	}
	newURL := "https://" + strings.TrimSpace(state.Backend.Domain)
	var failed []string
	for i := range targets {
		ag := targets[i]
		if os.Getenv("WG_LEGACY_ROUTER_SSH") != "1" {
			PrintStep(1, 1, "Re-enroll через AWG Manager/KeenDNS: "+ag.Nickname)
			if err := actionInstallAgentAWGM(state, secrets, NewDownloader(), ag.Nickname); err != nil {
				msg := fmt.Sprintf("%s: awg manager re-enroll: %v", ag.Nickname, err)
				PrintWarn(msg)
				failed = append(failed, msg)
			}
			continue
		}
		allowReEnroll := os.Getenv("WG_YES_TO_ALL") == "1"
		tokenEnv := agentTokenEnv(ag.Nickname)
		if secrets.GetNonInteractive(tokenEnv) == "" && !allowReEnroll {
			PrintWarn(fmt.Sprintf("%s: нет %s в env/disk-cache", ag.Nickname, tokenEnv))
			PrintWarn("Старый raw-токен восстановить нельзя: в DB хранится только token_hash.")
			ans := strings.ToLower(strings.TrimSpace(Ask("создать новый токен и переписать config.yaml для "+ag.Nickname+"? [y/N]", "n")))
			allowReEnroll = ans == "y" || ans == "yes" || ans == "д" || ans == "да"
		}
		plan, err := planMigrationToken(secrets, ag, allowReEnroll, generateAgentRawToken)
		if err != nil {
			msg := fmt.Sprintf("%s: token: %v", ag.Nickname, err)
			PrintWarn(msg)
			failed = append(failed, msg)
			continue
		}

		PrintStep(1, 2, "VPS users DB: "+ag.Nickname)
		if err := vpsUpsertMigratedUser(bs, ag, hashAgentRawToken(plan.RawToken)); err != nil {
			msg := fmt.Sprintf("%s: users DB: %v", ag.Nickname, err)
			PrintWarn(msg)
			failed = append(failed, msg)
			continue
		}
		if plan.ReEnroll {
			os.Setenv(plan.TokenEnv, plan.RawToken)
			if err := saveReEnrolledAgentToken(secrets, plan); err != nil {
				msg := fmt.Sprintf("%s: save token: %v", ag.Nickname, err)
				PrintWarn(msg)
				failed = append(failed, msg)
				continue
			}
		}

		PrintStep(2, 2, "Router config.yaml: "+ag.Nickname)
		if err := migrateAgentBackendConfig(state, secrets, &ag, newURL, plan.RawToken); err != nil {
			msg := fmt.Sprintf("%s: router config: %v", ag.Nickname, err)
			PrintWarn(msg)
			failed = append(failed, msg)
			continue
		}
		if plan.ReEnroll {
			PrintOK(ag.Nickname + " перенесён на " + newURL + " с новым agent token")
		} else {
			PrintOK(ag.Nickname + " перенесён на " + newURL)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("migration incomplete: %s", strings.Join(failed, "; "))
	}
	return nil
}

type migrationTokenPlan struct {
	TokenEnv string
	RawToken string
	ReEnroll bool
}

func agentTokenEnv(nickname string) string {
	return "WG_AGENT_TOKEN_" + strings.ToUpper(nickname)
}

func planMigrationToken(secrets *SecretStore, ag AgentState, allowReEnroll bool, generate func() (string, error)) (migrationTokenPlan, error) {
	tokenEnv := agentTokenEnv(ag.Nickname)
	rawToken := ""
	if secrets != nil {
		rawToken = secrets.GetNonInteractive(tokenEnv)
	}
	if rawToken != "" {
		return migrationTokenPlan{TokenEnv: tokenEnv, RawToken: rawToken}, nil
	}
	if !allowReEnroll {
		return migrationTokenPlan{}, fmt.Errorf("нет %s в env/disk-cache", tokenEnv)
	}
	rawToken, err := generate()
	if err != nil {
		return migrationTokenPlan{}, err
	}
	return migrationTokenPlan{TokenEnv: tokenEnv, RawToken: rawToken, ReEnroll: true}, nil
}

func saveReEnrolledAgentToken(secrets *SecretStore, plan migrationTokenPlan) error {
	if secrets == nil {
		return nil
	}
	if err := secrets.Set(plan.TokenEnv, plan.RawToken); err != nil {
		if errors.Is(err, ErrCacheDisabled) {
			PrintWarn("WG_NO_SECRET_CACHE=1 — raw-token не сохранён на диск. Сохрани вручную:")
			fmt.Printf("    %s=%s\n", plan.TokenEnv, plan.RawToken)
			return nil
		}
		return err
	}
	PrintOK(plan.TokenEnv + " сохранён в локальный disk-кэш")
	return nil
}

func migrationTargets(state *State, nickname string) ([]AgentState, error) {
	if nickname != "" {
		ag := state.FindAgent(nickname)
		if ag == nil {
			return nil, fmt.Errorf("agent %q not found in wizard.toml", nickname)
		}
		return []AgentState{*ag}, nil
	}
	if len(state.Agents) == 0 {
		return nil, fmt.Errorf("no agents in wizard.toml")
	}
	out := make([]AgentState, len(state.Agents))
	copy(out, state.Agents)
	return out, nil
}

func migrateAgentBackendConfig(state *State, secrets *SecretStore, ag *AgentState, backendURL, rawToken string) error {
	pass, err := agentRouterPassword(secrets, ag.Nickname)
	if err != nil {
		return err
	}
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	rep, cleanup, iface, perr := runPathDiscoveryStepWithNetfix(state, ag, secrets)
	if cleanup != nil {
		defer cleanup()
	}
	if perr != nil {
		return fmt.Errorf("path discovery: %w", perr)
	}
	if rep.Chosen == nil {
		return diagnoseUnreachable(state, ag, rep, secrets)
	}
	if iface != "" && iface != ag.PreferredIface {
		if live := state.FindAgent(ag.Nickname); live != nil {
			live.PreferredIface = iface
		}
		ag.PreferredIface = iface
	}

	s, err := connectAgentSSHManaged(state, ag, pass, kh, "migrate-backend")
	if err != nil {
		return err
	}
	defer s.Close()
	if err := verifyExpectedMAC(s, ag.ExpectedMAC); err != nil {
		return err
	}
	existingNick := stepReadExistingAgentNickname(s)
	if existingNick != "" && existingNick != ag.Nickname {
		return fmt.Errorf("router already hosts agent %q, refusing to overwrite as %q", existingNick, ag.Nickname)
	}
	if _, err := s.MustRun("mkdir -p /opt/etc/wg-monitor"); err != nil {
		return err
	}
	cfg, err := RenderAgentYAML(AgentParams{
		BackendURL: backendURL,
		Token:      rawToken,
		Nickname:   ag.Nickname,
	})
	if err != nil {
		return err
	}
	s.Run("cp -p /opt/etc/wg-monitor/config.yaml /opt/etc/wg-monitor/config.yaml.bak-migrate-$(date +%Y%m%d%H%M%S) 2>/dev/null || true")
	if err := s.UploadStdin("/opt/etc/wg-monitor/config.yaml", cfg); err != nil {
		return err
	}
	if _, err := s.MustRun("chmod 600 /opt/etc/wg-monitor/config.yaml"); err != nil {
		return err
	}
	if _, err := s.MustRun("/opt/etc/init.d/S99wg-monitor restart 2>/dev/null || { /opt/etc/init.d/S99wg-monitor stop 2>/dev/null; /opt/etc/init.d/S99wg-monitor start; }"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	pid, _ := s.MustRun("pidof wg-monitor")
	if strings.TrimSpace(pid) == "" {
		return fmt.Errorf("agent did not start after config rewrite")
	}
	PrintOK("wg-monitor запущен (PID " + strings.TrimSpace(pid) + ")")
	return nil
}

func agentRouterPassword(secrets *SecretStore, nickname string) (string, error) {
	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
	pass, _ := secrets.Get(envName, "пароль root для "+nickname, &MemoryFileLookup{
		Path:    memFile,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if pass == "" {
		pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
	}
	if pass == "" {
		return "", fmt.Errorf("missing router password for %s", nickname)
	}
	return pass, nil
}

// cliNicknameRe mirrors wg-monitor-cli's own regex (cmd/wg-monitor-cli/main.go).
// The CLI rejects anything else, so we validate up-front to avoid a confusing
// error after the nickname is already collected.
var cliNicknameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,15}$`)

// vpsUserExists checks /var/lib/wg-monitor/state.db for a row with the given
// nickname via sqlite3 (which is part of the standard VPS toolchain — same
// binary the CLI uses). Returns (exists, err); a missing DB or sqlite3 binary
// is treated as a real error so the caller doesn't silently fall through to
// vpsAddUser and clobber an existing row.
//
// SQL string is built with shell single-quote escaping for the outer shell
// (sqlite3 is invoked via SSH `sh -c`) AND SQL-double-single-quote for the
// inner SELECT — both layers needed because the value crosses both. Today
// nickname is regex-restricted (cliNicknameRe = ^[a-z][a-z0-9_-]{1,15}$,
// no quotes possible), but consistent escaping keeps this safe even if the
// regex is loosened later.
func vpsUserExists(bs *SSH, nick string) (bool, error) {
	sqlNick := strings.ReplaceAll(nick, "'", "''")
	cmd := fmt.Sprintf(
		`sqlite3 /var/lib/wg-monitor/state.db %s`,
		shellSingleQuote(fmt.Sprintf("SELECT 1 FROM users WHERE nickname = '%s' LIMIT 1;", sqlNick)),
	)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return false, fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return false, fmt.Errorf("sqlite3 rc=%d stderr=%q", rc, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(out) == "1", nil
}

func ensureRemoteSQLite3(bs *SSH) error {
	if _, _, rc, _ := bs.Run("command -v sqlite3 >/dev/null 2>&1"); rc == 0 {
		return nil
	}
	PrintWarn("sqlite3 не установлен на VPS — ставлю пакет sqlite3 для работы с users DB")
	cmd := "export DEBIAN_FRONTEND=noninteractive; apt-get update && apt-get install -y sqlite3"
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return fmt.Errorf("install sqlite3 ssh transport: %w", err)
	}
	if rc != 0 {
		return fmt.Errorf("install sqlite3 rc=%d stderr=%q stdout=%q", rc, strings.TrimSpace(stderr), strings.TrimSpace(out))
	}
	PrintOK("sqlite3 установлен")
	return nil
}

func generateAgentRawToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func hashAgentRawToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func stageAgentRawToken(secrets *SecretStore, tokenEnv, rawToken string) error {
	os.Setenv(tokenEnv, rawToken)
	if secrets == nil {
		return nil
	}
	return secrets.Set(tokenEnv+"_STAGED", rawToken)
}

func buildUpdateAgentTokenHashSQL(nick, tokenHash string) string {
	sqlNick := strings.ReplaceAll(nick, "'", "''")
	sqlHash := strings.ReplaceAll(tokenHash, "'", "''")
	return fmt.Sprintf("UPDATE users SET token_hash = '%s' WHERE nickname = '%s'; SELECT changes();", sqlHash, sqlNick)
}

func vpsUpdateAgentTokenHash(bs *SSH, nick, tokenHash string) error {
	if !cliNicknameRe.MatchString(nick) {
		return fmt.Errorf("invalid nickname %q", nick)
	}
	if len(tokenHash) != 64 {
		return fmt.Errorf("invalid token hash length for %s", nick)
	}
	cmd := fmt.Sprintf(
		`sqlite3 /var/lib/wg-monitor/state.db %s`,
		shellSingleQuote(buildUpdateAgentTokenHashSQL(nick, tokenHash)),
	)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return fmt.Errorf("sqlite3 update token_hash rc=%d stderr=%q", rc, strings.TrimSpace(stderr))
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != "1" {
		return fmt.Errorf("sqlite3 update token_hash affected %q rows", strings.TrimSpace(out))
	}
	PrintOK("users DB: token_hash обновлён для " + nick)
	return nil
}

func vpsUpsertMigratedUser(bs *SSH, ag AgentState, tokenHash string) error {
	if !cliNicknameRe.MatchString(ag.Nickname) {
		return fmt.Errorf("invalid nickname %q", ag.Nickname)
	}
	if len(tokenHash) != 64 {
		return fmt.Errorf("invalid token hash length for %s", ag.Nickname)
	}
	cmd := fmt.Sprintf(
		`sqlite3 /var/lib/wg-monitor/state.db %s`,
		shellSingleQuote(buildMigrateUserUpsertSQL(ag, tokenHash)),
	)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return fmt.Errorf("sqlite3 upsert user rc=%d stderr=%q", rc, strings.TrimSpace(stderr))
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != "1" {
		return fmt.Errorf("sqlite3 upsert user affected %q rows", strings.TrimSpace(out))
	}
	PrintOK("users DB: " + ag.Nickname + " восстановлен")
	return nil
}

func buildMigrateUserUpsertSQL(ag AgentState, tokenHash string) string {
	kind := strings.TrimSpace(ag.Kind)
	if kind != "mobile" {
		kind = "static"
	}
	return fmt.Sprintf(
		`INSERT INTO users(nickname, token_hash, expected_exit_ip, awg_iface, kind, telegram_thread_id, ssh_host, ssh_port, ssh_user, arch, last_deployed_version, deploy_ring, pending_version, pending_since)
VALUES (%s, %s, '0.0.0.0', 'awg0', %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
ON CONFLICT(nickname) DO UPDATE SET
  token_hash=excluded.token_hash,
  kind=excluded.kind,
  telegram_thread_id=COALESCE(excluded.telegram_thread_id, users.telegram_thread_id),
  ssh_host=excluded.ssh_host,
  ssh_port=excluded.ssh_port,
  ssh_user=excluded.ssh_user,
  arch=excluded.arch,
  last_deployed_version=excluded.last_deployed_version,
  deploy_ring=excluded.deploy_ring,
  pending_version=excluded.pending_version,
  pending_since=excluded.pending_since;
SELECT changes();`,
		sqlString(ag.Nickname),
		sqlString(tokenHash),
		sqlString(kind),
		sqlNullableInt(ag.ThreadID),
		sqlNullableString(ag.Host),
		sqlNullableInt(portOrDefault(ag.Port, 222)),
		sqlNullableString(userOrDefault(ag.User, "root")),
		sqlNullableString(ag.Arch),
		sqlNullableString(ag.LastDeployedVersion),
		sqlNullableString(ag.Ring),
		sqlNullableString(ag.PendingVersion),
		sqlNullableString(ag.PendingSince),
	)
}

func sqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sqlNullableString(s string) string {
	if strings.TrimSpace(s) == "" {
		return "NULL"
	}
	return sqlString(s)
}

func sqlNullableInt(n int) string {
	if n == 0 {
		return "NULL"
	}
	return fmt.Sprint(n)
}

func chooseAgentForAction(state *State, nickname, label string) (*AgentState, error) {
	if nickname != "" {
		ag := state.FindAgent(nickname)
		if ag == nil {
			return nil, fmt.Errorf("agent %q not found in wizard.toml", nickname)
		}
		return ag, nil
	}
	switch len(state.Agents) {
	case 0:
		return nil, fmt.Errorf("no agents in wizard.toml")
	case 1:
		return &state.Agents[0], nil
	}
	fmt.Println("Выбери роутер для " + label + ":")
	for i, ag := range state.Agents {
		fmt.Printf("  [%d] %s (%s:%d)\n", i+1, ag.Nickname, ag.Host, portOrDefault(ag.Port, 222))
	}
	idx := parseIntOr(Ask("номер", "1"), 1)
	if idx < 1 || idx > len(state.Agents) {
		idx = 1
	}
	return &state.Agents[idx-1], nil
}

// vpsAddUser invokes /usr/local/bin/wg-monitor-cli add-user on the VPS and
// extracts the raw token from its stdout. The CLI prints exactly one
// "Token (raw, save now — only shown once): <hex>\n" line — see
// cmd/wg-monitor-cli/main.go runAddUser. Returns an actionable error if the
// CLI binary isn't installed (operator either ran wizard before that release
// or wiped /usr/local/bin).
//
// awg_iface / expected_exit_ip are passed as fixed placeholders: both fields
// are deprecated in the agent (silently ignored after the awg-manager pivot,
// see internal/agent/config.go::AWGCheckConfig), but wg-monitor-cli still
// requires non-empty values for the DB schema. Kind ("static"/"mobile") IS
// load-bearing: backend's heartbeat watcher uses StaleAfterMobileSec for
// mobile-kind users.
func vpsAddUser(bs *SSH, nick, kind string) (string, error) {
	if _, _, rc, _ := bs.Run("test -x /usr/local/bin/wg-monitor-cli"); rc != 0 {
		return "", fmt.Errorf(
			"/usr/local/bin/wg-monitor-cli не установлен на VPS — на этом VPS он добавлялся вручную. " +
				"Поставь его вручную (scp wg-monitor-cli-linux-amd64 → /usr/local/bin/wg-monitor-cli, chmod +x) и повтори [3].")
	}
	cmd := fmt.Sprintf(
		`/usr/local/bin/wg-monitor-cli add-user --nickname=%s --awg-iface=awg0 --expected-exit-ip=0.0.0.0 --kind=%s`,
		shellSingleQuote(nick), shellSingleQuote(kind),
	)
	out, stderr, rc, err := bs.Run(cmd)
	if err != nil {
		return "", fmt.Errorf("ssh transport: %w", err)
	}
	if rc != 0 {
		return "", fmt.Errorf("wg-monitor-cli add-user rc=%d stderr=%q", rc, strings.TrimSpace(stderr))
	}
	tok := extractRawTokenFromAddUserOutput(out)
	if tok == "" {
		return "", fmt.Errorf("wg-monitor-cli add-user отработал rc=0, но raw-токен не найден в stdout: %q", out)
	}
	PrintOK("wg-monitor-cli add-user выполнился, raw-токен получен")
	return tok, nil
}

// addUserTokenLineRe matches the raw-token line printed by wg-monitor-cli.
// Format is fixed (cmd/wg-monitor-cli/main.go:113). Token is 64 hex chars
// (32-byte rand → hex.EncodeToString).
var addUserTokenLineRe = regexp.MustCompile(`Token \(raw, save now[^)]*\):\s*([0-9a-fA-F]{64})`)

func extractRawTokenFromAddUserOutput(stdout string) string {
	m := addUserTokenLineRe.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// shellSingleQuote wraps s in POSIX single quotes for safe inclusion in a
// remote `sh -c` command. ASCII-only inputs (validated upstream) make the
// escape trivial — we just close the quote, emit a backslash-quote, and
// reopen.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// autoCreateForumTopic tries to create a Telegram forum topic for the new
// router via createForumTopic Bot API. Returns 0 on any failure (missing
// bot token, missing chat_id, network error, bot lacks manage_topics, chat
// is not a forum) — caller should fall back to a manual prompt.
//
// Failure printing is best-effort PrintWarn; the calling flow stays alive.
func autoCreateForumTopic(state *State, secrets *SecretStore, nick string) int {
	if state.Telegram.ChatID == 0 {
		PrintWarn("telegram chat_id не задан в wizard.toml — не могу создать топик автоматически")
		return 0
	}
	tok, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token", nil)
	if tok == "" {
		PrintWarn("WG_BOT_TOKEN не задан — не могу создать топик автоматически")
		return 0
	}
	cli := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   tok,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := cli.CreateForumTopic(ctx, state.Telegram.ChatID, nick, 0)
	if err != nil {
		PrintWarn("createForumTopic не удался (" + err.Error() + ") — спрошу thread_id вручную")
		return 0
	}
	PrintOK(fmt.Sprintf("создан топик thread_id=%d", id))
	return int(id)
}

// --- helpers ---

func parseIntOr(s string, def int) int {
	if s == "" {
		return def
	}
	var n int
	fmt.Sscanf(s, "%d", &n)
	if n == 0 {
		return def
	}
	return n
}

func parseInt64Or(s string, def int64) int64 {
	if s == "" {
		return def
	}
	var n int64
	fmt.Sscanf(s, "%d", &n)
	return n
}

func strOrDefault(n int, def string) string {
	if n == 0 {
		return def
	}
	return fmt.Sprintf("%d", n)
}

func strOrDefaultS(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func randomHexToken(nBytes int) string {
	b := make([]byte, nBytes)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// actionSyncVPS pulls the fleet list from /v1/wizard/agents and merges into
// state.Agents. Best-effort: prints what changed; never deletes local-only
// entries (warns instead).
func actionSyncVPS(state *State, secrets *SecretStore) error {
	if state.Backend.Domain == "" {
		return fmt.Errorf("backend.domain пустой — сначала [1] install-backend")
	}
	tok, _ := secrets.Get("WIZARD_TOKEN", "Wizard sync token (из /etc/wg-monitor/wizard-token.txt на VPS)", nil)
	if tok == "" {
		return fmt.Errorf("WIZARD_TOKEN не задан")
	}
	c := NewVPSClientForBackend(state, tok, 15*time.Second)
	if c == nil {
		return fmt.Errorf("VPSClient init failed (empty domain or token)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	remote, err := c.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("VPS unreachable or auth failed: %w", err)
	}
	merged, added, divergent := MergeAgents(state.Agents, remote)
	state.Agents = merged
	PrintOK(fmt.Sprintf("Получено с VPS: %d роутеров", len(remote)))
	if len(added) > 0 {
		PrintInfo("Добавлено локально:")
		for _, n := range added {
			PrintInfo("  + " + n)
		}
	}
	if len(divergent) > 0 {
		PrintWarn("SSH-координаты разошлись (VPS-значение применено):")
		for _, n := range divergent {
			PrintWarn("  ~ " + n)
		}
	}
	// Local-only detection: nicknames present locally but not in remote.
	remoteSet := make(map[string]struct{}, len(remote))
	for _, r := range remote {
		remoteSet[r.Nickname] = struct{}{}
	}
	var localOnly []string
	for _, a := range state.Agents {
		if _, ok := remoteSet[a.Nickname]; !ok {
			localOnly = append(localOnly, a.Nickname)
		}
	}
	if len(localOnly) > 0 {
		PrintWarn("Локально есть, на VPS нет (возможно удалены через CLI):")
		for _, n := range localOnly {
			PrintWarn("  ? " + n)
		}
	}
	return nil
}

// pushToVPSBestEffort PUTs deploy info for the given agent. Logs but does
// NOT return errors — push is best-effort and must never break the deploy
// flow (e.g. when offline or token rotation pending).
func pushToVPSBestEffort(state *State, secrets *SecretStore, a AgentState) {
	if state.Backend.Domain == "" {
		return
	}
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	if tok == "" {
		return
	}
	c := NewVPSClientForBackend(state, tok, 10*time.Second)
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.PushAgent(ctx, AgentStateToRemote(a)); err != nil {
		PrintWarn(fmt.Sprintf("VPS sync push failed for %s: %v (deploy itself succeeded)", a.Nickname, err))
		return
	}
	PrintOK("VPS sync: " + a.Nickname)
}

// detectDoubleDeploy returns true when ≥2 reachable candidates have the
// same `target` nickname installed on them — the canonical signature of
// "operator deployed to wrong box, then deployed to right box too".
// `existingNicks` is keyed by candidate iface name; values come from
// stepReadExistingAgentNickname run over SSH on each candidate.
func detectDoubleDeploy(rep *PathReport, existingNicks map[string]string, target string) bool {
	if rep == nil || target == "" {
		return false
	}
	count := 0
	for _, c := range rep.Candidates {
		if !c.Responded() {
			continue
		}
		if existingNicks[c.Iface] == target {
			count++
		}
	}
	return count >= 2
}

// cleanupAgentPaths returns the ordered list of shell commands the
// uninstall flow runs to remove every artefact of a wg-monitor agent
// install from a Keenetic router. Pure (no side effects), tested
// directly. Order matters — stop the daemon BEFORE removing its binary,
// or busybox init may re-spawn it mid-rm.
func cleanupAgentPaths() []string {
	return []string{
		"/opt/etc/init.d/S99wg-monitor stop 2>/dev/null; true",
		"killall -9 wg-monitor 2>/dev/null; true",
		"sleep 1",
		"rm -f /opt/bin/wg-monitor /opt/bin/wg-monitor.bak /opt/bin/wg-monitor.new",
		"rm -rf /opt/etc/wg-monitor",
		"rm -f /opt/etc/init.d/S99wg-monitor",
		"rm -rf /opt/var/wg-monitor",
	}
}

// UninstallTarget describes which router to clean. EITHER the named agent
// is resolved from state.Agents, OR explicit Host/Port/User are provided
// (operator uninstalling from a router that's not in wizard.toml — typical
// "I accidentally installed on local box" scenario).
type UninstallTarget struct {
	Nickname string // optional
	Host     string
	Port     int
	User     string
}

// actionUninstallAgent removes a wg-monitor agent from a router after
// double confirmation. Does NOT touch the VPS users-table — the token
// stays valid so re-install on the correct box proceeds normally.
//
// Optionally also clears ExpectedMAC + PreferredIface in wizard.toml so
// the next install-agent under the same nickname can pin a fresh box.
func actionUninstallAgent(state *State, secrets *SecretStore, target UninstallTarget) error {
	host, port, user := target.Host, target.Port, target.User
	var ag *AgentState
	if target.Nickname != "" {
		ag = state.FindAgent(target.Nickname)
		if ag != nil {
			if host == "" {
				host = ag.Host
			}
			if port == 0 {
				port = ag.Port
			}
			if user == "" {
				user = ag.User
			}
		}
	}
	if host == "" {
		host = orDefault(Ask("Хост роутера", "192.168.31.1"), "192.168.31.1")
	}
	if port == 0 {
		port = parseIntOr(Ask("SSH port", "222"), 222)
	}
	if user == "" {
		user = orDefault(Ask("SSH user", "root"), "root")
	}

	rep, cleanup, _, err := runPathDiscoveryStep(host, port, "", NewRealProber())
	defer cleanup()
	if err != nil {
		PrintFail("path discovery: " + err.Error())
		return err
	}
	if rep.Chosen == nil {
		PrintFail("роутер недоступен — нечего сносить")
		return fmt.Errorf("router %s unreachable", host)
	}

	envName := ""
	if ag != nil {
		envName = "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	}
	pass := ""
	if envName != "" {
		pass = secrets.GetNonInteractive(envName)
	}
	if pass == "" {
		pass = AskSecret("пароль root для " + host)
	}
	if pass == "" {
		return fmt.Errorf("missing password")
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	alias := host
	if ag != nil {
		alias = ag.Nickname
	}
	s, err := ConnectSSH(host, port, user, pass, kh, alias)
	if err != nil {
		PrintFail("SSH: " + err.Error())
		return err
	}
	defer s.Close()

	hostname := strings.TrimSpace(stepReadOrEmpty(s, "cat /proc/sys/kernel/hostname 2>/dev/null || uname -n"))
	mac := extractMAC(stepDetectPrimaryMAC(s))
	existingNick := stepReadExistingAgentNickname(s)
	if hostname == "" {
		hostname = "?"
	}
	if mac == "" {
		mac = "?"
	}
	PrintInfo(fmt.Sprintf("на этом роутере: hostname=%q mac=%s agent_nickname=%q", hostname, mac, existingNick))
	if existingNick == "" {
		PrintWarn("на роутере нет /opt/etc/wg-monitor/config.yaml — возможно агента уже нет, но пройду по cleanup-списку")
	}

	ans := strings.ToLower(strings.TrimSpace(Ask(
		fmt.Sprintf("Снести агента с этого роутера (%s, %s)? [y/N]", hostname, mac), "")))
	if ans != "y" && ans != "yes" && ans != "д" && ans != "да" {
		PrintInfo("отмена")
		return nil
	}

	steps := cleanupAgentPaths()
	for i, cmd := range steps {
		PrintStep(i+1, len(steps), cmd)
		if _, _, _, err := s.Run(cmd); err != nil {
			PrintWarn(fmt.Sprintf("step %d failed (продолжаю): %v", i+1, err))
		}
	}

	if out, _, _, _ := s.Run("pidof wg-monitor"); strings.TrimSpace(out) != "" {
		PrintWarn("pidof wg-monitor всё ещё что-то возвращает (PID " + strings.TrimSpace(out) + ") — проверь вручную")
	} else {
		PrintOK("процесс wg-monitor не запущен")
	}
	if _, _, rc, _ := s.Run("test -f /opt/bin/wg-monitor"); rc == 0 {
		PrintWarn("/opt/bin/wg-monitor всё ещё на месте — что-то пошло не так")
	} else {
		PrintOK("/opt/bin/wg-monitor удалён")
	}

	if ag != nil && (ag.ExpectedMAC != "" || ag.PreferredIface != "") {
		ans := strings.ToLower(strings.TrimSpace(Ask(
			"Сбросить expected_mac/preferred_iface для "+ag.Nickname+" в wizard.toml? [y/N]", "")))
		if ans == "y" || ans == "yes" || ans == "д" || ans == "да" {
			ag.ExpectedMAC = ""
			ag.PreferredIface = ""
			PrintOK("wizard.toml: expected_mac и preferred_iface сброшены для " + ag.Nickname)
		}
	}
	return nil
}
