package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	if rep.Multiple && os.Getenv("WG_YES_TO_ALL") != "1" {
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
		_ = preferred
		PrintOK("использую " + chosenName + " (" + rep.Chosen.LocalIP + ", " + fmt.Sprint(rep.Chosen.Latency.Milliseconds()) + "мс)")
	}
	return rep, cleanup, chosenName, nil
}

// diagnoseUnreachable handles "Layer 1 found no responding path". Fetches
// VPS heartbeat for the agent (if state allows), classifies the candidate
// failure modes, prints a one-shot diagnostic, returns a generic err so
// the caller doesn't fall into SSH-retry loop.
func diagnoseUnreachable(state *State, ag *AgentState, rep *PathReport, secrets *SecretStore) error {
	hb := ""
	if state.Backend.Domain != "" {
		if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
			if c := NewVPSClient(state.Backend.Domain, tok); c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				hb = c.HeartbeatStatus(ctx, ag.Nickname)
				cancel()
			}
		}
	}
	PrintFail(diagnosisFromReport(rep, hb))
	return fmt.Errorf("router %s unreachable", ag.Host)
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

	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		PrintFail("пароль обязателен")
		return fmt.Errorf("missing password")
	}

	khPath := defaultCacheDir() + "/known_hosts"
	kh, err := NewKnownHosts(khPath)
	if err != nil {
		return err
	}

	port := state.Backend.Port
	if port == 0 {
		port = 22
	}
	user := state.Backend.User
	if user == "" {
		user = "root"
	}

	PrintStep(1, 4, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, port, user, pass, kh, "backend")
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
		url := "https://" + state.Backend.Domain + "/healthz"
		if err := stepVerifyHTTP(s, url); err != nil {
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
// backend.yaml is empty/unreadable/missing, and the deploy wizard's [5] sync
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
		PrintOK("wizard endpoint: 401 (auth required, ready for [5] Sync)")
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
	// случался ([5] VPS Sync даёт запись с пустыми SSH-полями и пустой версией,
	// или сюда вручную добавили [[agents]] блок), update сломает роутер: стопнет
	// агента и попытается стартануть несуществующий S99-скрипт. Bail заранее,
	// до prompt'а пароля.
	if ag.LastDeployedVersion == "" {
		PrintFail(fmt.Sprintf(
			"%s ещё ни разу не устанавливался (last_deployed_version пуст в wizard.toml).\n"+
				"  Это update-flow, он только подменяет бинарь. Для первого install запусти [3] Установить агента.",
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

	rep, cleanup, iface, perr := runPathDiscoveryStep(ag.Host, portOrDefault(ag.Port, 222), ag.PreferredIface, NewRealProber())
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
	s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh, ag.Nickname)
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
		s, err = ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
		if err != nil {
			return err
		}
	}
	defer s.Close()
	if err := stepCheckSSH(s, ag.Host); err != nil {
		return err
	}

	// Defence-in-depth: state может врать (last_deployed_version выставлен
	// руками, или /opt был вайпнут после reset). Если init-скрипта нет, не
	// продолжаем — kill+swap+start с несуществующим S99 заведомо сломает
	// агента, оставив роутер молчащим.
	if _, _, rc, _ := s.Run("test -x /opt/etc/init.d/S99wg-monitor"); rc != 0 {
		PrintFail(fmt.Sprintf(
			"на %s нет /opt/etc/init.d/S99wg-monitor — агент не установлен на роутере, хотя в wizard.toml last_deployed_version=%q.\n"+
				"  Возможно, /opt был вайпнут или агент ставился без wizard'а. Запусти [3] Установить агента.",
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

	// Нормализация SSH-полей после успешного подключения. До этой точки оператор
	// мог иметь ag.User=="" / ag.Port==0 (например, агент попал в state через
	// [5] VPS Sync, где remote SSHUser=NULL и MergeAgents оставляет local пустым).
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

func actionInstallAgent(state *State, secrets *SecretStore, dl *Downloader, nickname string) error {
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
		// ввёл nickname агента, добавленного через [4] — переиспользуем
		// существующий entry вместо создания дубликата в state.Agents.
		ag = state.FindAgent(nick)
		if ag == nil {
			state.Agents = append(state.Agents, AgentState{Nickname: nick})
			ag = &state.Agents[len(state.Agents)-1]
		}
	}

	// Каждый Ask условный — поле уже могли заполнить в state (через
	// actionAddRouter, или вручную в wizard.toml, или прошлый прогон).
	// Цель — нулевые prompt'ы на повторных запусках и при вызове из [4].
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
	// env (выставляет [4] в текущем процессе) + disk-кэш (выставляет [4]
	// между сессиями). Случайно сгенерить новый токен здесь нельзя —
	// раскорреляция с DB (token_hash на VPS не совпадёт) → backend
	// будет отвергать heartbeat'ы агента молча.
	tok := secrets.GetNonInteractive(tokenEnv)
	if tok == "" {
		PrintFail(fmt.Sprintf(
			"токен %s не найден ни в env, ни в disk-кэше.\n"+
				"  Это значит, что %s ещё не зарегистрирован в DB на VPS.\n"+
				"  Запусти [4] Добавить новый роутер — wizard сам создаст user'а через wg-monitor-cli\n"+
				"  и сохранит raw-токен в disk-кэш. После этого можно использовать [3] для переустановки.",
			tokenEnv, ag.Nickname))
		return fmt.Errorf("agent token for %s not in cache; use [4] Add Router first", ag.Nickname)
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	rep2, cleanup2, iface2, perr2 := runPathDiscoveryStep(ag.Host, portOrDefault(ag.Port, 222), ag.PreferredIface, NewRealProber())
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
	s, err := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
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
		s, err = ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
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
	rel, err := dl.GetLatestRelease()
	if err != nil {
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	// 1. Запросить параметры
	state.Backend.Host = Ask("VPS host или IP", state.Backend.Host)
	state.Backend.Port = parseIntOr(Ask("SSH port", strOrDefault(state.Backend.Port, "22")), 22)
	state.Backend.User = orDefault(Ask("SSH user", strOrDefaultS(state.Backend.User, "root")), "root")
	state.Backend.Domain = Ask("Домен бэкенда (например wgmon.example.com)", state.Backend.Domain)
	caddyEmail := Ask("Email для Let's Encrypt", "admin@"+state.Backend.Domain)

	if state.Telegram.ChatID == 0 {
		state.Telegram.ChatID = parseInt64Or(Ask("Telegram chat_id (отрицательное число)", ""), 0)
	}
	if state.Telegram.AdminUserID == 0 {
		state.Telegram.AdminUserID = parseInt64Or(Ask("Telegram admin user_id (твой User ID)", ""), 0)
	}

	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token (1234:ABC...)", nil)

	if pass == "" || botToken == "" || state.Backend.Host == "" || state.Backend.Domain == "" {
		PrintFail("обязательные поля пустые")
		return fmt.Errorf("missing required fields")
	}

	// 2. SSH
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 14, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh, "backend")
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()

	// Pre-flight: detect existing install and require explicit confirmation.
	// install-backend перезаписывает backend.yaml, bot-token.txt и unit-файл
	// — на работающем backend'е без подтверждения это может молча сломать
	// прод (другой chat_id/admin_user_id, устаревший токен из disk-cache, и т.п.).
	if existingInstallDetected(s) {
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
	if err := stepUploadFile(s, "/etc/wg-monitor/backend.yaml", yamlBytes, "600"); err != nil {
		return err
	}

	PrintStep(5, 14, "bot-token.txt")
	// Отдельный файл, потому что backend.yaml имеет mode 600 root:wgmonitor —
	// читаемый отладчиком. Токен бота — нет: только wgmonitor.
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
	if err := stepUploadFile(s, "/etc/wg-monitor/bot-token.txt", []byte(newBot), "600"); err != nil {
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
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", ""); err != nil {
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
	url := "https://" + state.Backend.Domain + "/healthz"
	if err := stepVerifyHTTP(s, url); err != nil {
		PrintWarn("health check не прошёл — возможно DNS ещё не прогрелся, проверь руками")
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
}

func actionAddRouter(state *State, secrets *SecretStore, dl *Downloader) error {
	if state.Backend.Host == "" {
		PrintFail("сначала install-backend (нужно куда добавлять)")
		return fmt.Errorf("no backend")
	}

	// Минимум интерактивности: единственный мандатный prompt — nickname.
	// Всё остальное wizard вытаскивает из state / secrets-кэша / разумных
	// дефолтов; password пробуется через env+cache+memory-файл и
	// запрашивается только если всё пусто.
	nick := Ask("Никнейм нового роутера (a-z, 2-16)", "")
	if nick == "" {
		return fmt.Errorf("nickname required")
	}
	if !cliNicknameRe.MatchString(nick) {
		PrintFail(fmt.Sprintf("nickname %q не подходит под ^[a-z][a-z0-9_-]{1,15}$ (требование wg-monitor-cli)", nick))
		return fmt.Errorf("invalid nickname")
	}

	// Регистрация в users-table на VPS через wg-monitor-cli.
	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		return fmt.Errorf("missing VPS password")
	}
	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")

	PrintStep(1, 3, "VPS: зарегистрировать "+nick+" в users DB")
	bs, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh, "backend")
	if err != nil {
		return err
	}
	defer bs.Close()

	// Источник истины по агентам — таблица users в /var/lib/wg-monitor/state.db.
	// CLI сам генерит raw-токен и хэширует в DB; из stdout вытягиваем raw-токен
	// (последний шанс его увидеть). Идемпотентности на стороне CLI нет: повтор
	// с тем же nickname упадёт на UNIQUE constraint, и raw мы тогда уже не
	// получим — поэтому сначала проверяем DB.
	tokEnv := "WG_AGENT_TOKEN_" + strings.ToUpper(nick)
	exists, err := vpsUserExists(bs, nick)
	if err != nil {
		return fmt.Errorf("check users table: %w", err)
	}
	var rawToken string
	switch {
	case exists && secrets.GetNonInteractive(tokEnv) != "":
		PrintInfo(fmt.Sprintf("%s уже в DB на VPS, беру токен из локального disk-кэша", nick))
		rawToken = secrets.GetNonInteractive(tokEnv)
	case exists:
		PrintFail(fmt.Sprintf(
			"%s уже в DB на VPS, но raw-токен утерян (token_hash необратим). Удали запись:\n"+
				"  ssh root@%s sqlite3 /var/lib/wg-monitor/state.db \"DELETE FROM users WHERE nickname='%s';\"\n"+
				"и запусти [4] заново.",
			nick, state.Backend.Host, nick))
		return fmt.Errorf("user exists in DB but token unknown")
	default:
		// Kind — единственное поле, которое реально влияет на поведение
		// backend'а (StaleAfterMobileSec для mobile). Берём из state'а
		// если оператор уже сохранял для этого nickname'а, иначе спрашиваем.
		// Дефолт "static" — оператор просто давит Enter для домашнего/офисного
		// роутера, mobile только для in-vehicle 4G.
		ag := state.FindAgent(nick)
		kind := "static"
		if ag != nil && ag.Kind != "" {
			kind = ag.Kind
		}
		kind = strings.ToLower(strings.TrimSpace(orDefault(Ask("Kind (static / mobile)", kind), kind)))
		if kind != "static" && kind != "mobile" {
			PrintFail("kind должен быть static или mobile")
			return fmt.Errorf("invalid kind")
		}
		rawToken, err = vpsAddUser(bs, nick, kind)
		if err != nil {
			return err
		}
		switch err := secrets.Set(tokEnv, rawToken); {
		case err == nil:
			PrintOK(fmt.Sprintf("токен %s сохранён в disk-кэш", tokEnv))
		case errors.Is(err, ErrCacheDisabled):
			// Disk-кэш выключен — токен живёт только в памяти процесса.
			// Если оператор Ctrl-C'нёт между этим шагом и установкой агента
			// на роутер, raw-токен потеряется навсегда (в DB только хэш).
			// Громкий warning + явная подсказка как сохранить.
			PrintWarn("WG_NO_SECRET_CACHE=1 — токен не записан на диск, живёт только в памяти текущего процесса")
			PrintWarn(fmt.Sprintf("СОХРАНИ СЕЙЧАС вручную, иначе при сбое retry потребует DELETE FROM users:"))
			fmt.Printf("    %s=%s\n", tokEnv, rawToken)
		default:
			PrintWarn("не смог сохранить токен в disk-кэш: " + err.Error())
		}
		// Сохраняем kind в state — на retry/reinstall не спрашиваем заново.
		if ag == nil {
			state.Agents = append(state.Agents, AgentState{Nickname: nick, Kind: kind})
		} else {
			ag.Kind = kind
		}
	}
	os.Setenv(tokEnv, rawToken)

	// Запись в wizard.toml. Дефолты подставляем здесь, чтобы actionInstallAgent
	// ниже по флоу не спрашивал host/port/user — happy path (стандартный
	// Keenetic) идёт без единого prompt'а кроме nickname'а + kind. SSH-fail
	// в actionInstallAgent поднимет интерактив только если дефолты не подошли.
	ag := state.FindAgent(nick)
	if ag == nil {
		state.Agents = append(state.Agents, AgentState{Nickname: nick, Kind: "static"})
		ag = &state.Agents[len(state.Agents)-1]
	}
	if ag.Host == "" {
		ag.Host = "192.168.31.1"
	}
	if ag.Port == 0 {
		ag.Port = 222
	}
	if ag.User == "" {
		ag.User = "root"
	}
	if ag.Kind == "" {
		ag.Kind = "static"
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
	return actionInstallAgent(state, secrets, dl, nick)
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
				"Поставь его вручную (scp wg-monitor-cli-linux-amd64 → /usr/local/bin/wg-monitor-cli, chmod +x) и повтори [4].")
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
	c := NewVPSClient(state.Backend.Domain, tok)
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
	c := NewVPSClient(state.Backend.Domain, tok)
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
