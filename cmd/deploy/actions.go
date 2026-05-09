package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"gopkg.in/yaml.v3"
)

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
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
	return nil
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

	rel, err := dl.GetLatestRelease()
	if err != nil {
		PrintFail("GitHub API: " + err.Error())
		return err
	}
	PrintOK("последний релиз: " + rel.TagName)

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-user-Projects-wg-monitor/memory/host_keenetic.md")
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

	PrintStep(1, 4, "SSH к роутеру "+ag.Nickname)
	s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh, ag.Nickname)
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()
	if err := stepCheckSSH(s, ag.Host); err != nil {
		return err
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
		state.Agents = append(state.Agents, AgentState{Nickname: nick})
		ag = &state.Agents[len(state.Agents)-1]
	}

	ag.Host = Ask("Хост роутера", strOrDefaultS(ag.Host, "192.168.0.1"))
	ag.Port = parseIntOr(Ask("SSH port", strOrDefault(ag.Port, "222")), 222)
	ag.User = orDefault(Ask("SSH user", strOrDefaultS(ag.User, "root")), "root")
	ag.AwgIface = orDefault(Ask("AmneziaWG iface", strOrDefaultS(ag.AwgIface, "awg0")), "awg0")
	ag.ExpectedExitIP = Ask("Expected exit IP (что bot должен видеть как public IP)", ag.ExpectedExitIP)
	if ag.ThreadID == 0 {
		ag.ThreadID = parseIntOr(Ask("Telegram thread_id топика этого роутера", "1"), 1)
	}

	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	home, _ := os.UserHomeDir()
	memFile := filepath.Join(home, ".claude/projects/c--Users-user-Projects-wg-monitor/memory/host_keenetic.md")
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
	tok := os.Getenv(tokenEnv)
	if tok == "" {
		PrintWarn("Токен агента не найден в " + tokenEnv + ".")
		PrintWarn("При install-backend он должен был сгенерироваться. Введи руками или сгенерирую новый:")
		tok = Ask("Token (Enter — сгенерировать новый)", "")
		if tok == "" {
			tok = randomHexToken(32)
			PrintWarn("новый токен: " + tok + " — сохрани в " + tokenEnv + " И в backend.yaml на VPS!")
		}
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}

	PrintStep(1, 7, "SSH к роутеру")
	s, err := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh, ag.Nickname)
	if err != nil {
		return err
	}
	defer s.Close()

	PrintStep(2, 7, "Архитектура")
	arch, err := stepDetectKeeneticArch(s)
	if err != nil {
		return err
	}
	ag.Arch = arch

	PrintStep(3, 7, "Директории /opt/{bin,etc/wg-monitor,etc/init.d,var/wg-monitor}")
	if _, err := s.MustRun("mkdir -p /opt/bin /opt/etc/wg-monitor /opt/etc/init.d /opt/var/wg-monitor"); err != nil {
		return err
	}
	PrintOK("ok")

	PrintStep(4, 7, "config.yaml")
	cfg, err := RenderAgentYAML(AgentParams{
		BackendURL:     "https://" + state.Backend.Domain,
		Token:          tok,
		Nickname:       ag.Nickname,
		AWGIface:       ag.AwgIface,
		ExpectedExitIP: ag.ExpectedExitIP,
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

	PrintStep(5, 7, "init.d скрипт")
	initd, err := ReadStaticTemplate("S99wg-monitor")
	if err != nil {
		return err
	}
	if err := s.UploadStdin("/opt/etc/init.d/S99wg-monitor", initd); err != nil {
		return err
	}
	s.MustRun("chmod +x /opt/etc/init.d/S99wg-monitor")
	PrintOK("/opt/etc/init.d/S99wg-monitor")

	PrintStep(6, 7, "Скачать агент бинарь")
	assetName := "wg-monitor-agent-linux-" + arch
	localPath, err := stepDownloadAsset(dl, rel, assetName)
	if err != nil {
		return err
	}

	PrintStep(7, 7, "Upload + start")
	if err := stepUploadAgentBinary(s, localPath, "/opt/bin/wg-monitor"); err != nil {
		return err
	}

	ag.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	ag.LastDeployedVersion = rel.TagName
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

	// Запросить хотя бы одного агента (для backend.yaml.agents)
	if len(state.Agents) == 0 {
		nick := Ask("Никнейм первого роутера (a-z, 2-16)", "testkeen")
		thread := parseIntOr(Ask("Telegram thread_id для топика этого роутера", "1"), 1)
		state.Agents = append(state.Agents, AgentState{
			Nickname: nick,
			ThreadID: thread,
		})
	}
	// Сгенерить токен агенту если ещё нет в env
	agentTokens := map[string]string{}
	for i := range state.Agents {
		ag := &state.Agents[i]
		envName := "WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname)
		tok := os.Getenv(envName)
		if tok == "" {
			tok = randomHexToken(32)
			PrintWarn(fmt.Sprintf("сгенерирован токен для %s — сохрани в %s", ag.Nickname, envName))
			fmt.Println("    " + tok)
		}
		agentTokens[ag.Nickname] = tok
	}

	// 2. SSH
	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 12, "SSH к VPS")
	s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh, "backend")
	if err != nil {
		PrintFail(err.Error())
		return err
	}
	defer s.Close()

	PrintStep(2, 12, "User wgmonitor")
	if err := stepEnsureUser(s, "wgmonitor"); err != nil {
		return err
	}

	PrintStep(3, 12, "Директории")
	stepEnsureDir(s, "/etc/wg-monitor", "")
	stepEnsureDir(s, "/var/lib/wg-monitor", "wgmonitor:wgmonitor")

	PrintStep(4, 12, "backend.yaml")
	var entries []AgentEntry
	for _, ag := range state.Agents {
		entries = append(entries, AgentEntry{
			Nickname: ag.Nickname,
			Token:    agentTokens[ag.Nickname],
			ThreadID: ag.ThreadID,
		})
	}
	yamlBytes, err := RenderBackendYAML(BackendParams{
		BotToken:    botToken,
		ChatID:      state.Telegram.ChatID,
		AdminUserID: state.Telegram.AdminUserID,
		Agents:      entries,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(s, "/etc/wg-monitor/backend.yaml", yamlBytes, "600"); err != nil {
		return err
	}

	PrintStep(5, 12, "systemd unit")
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

	PrintStep(6, 12, "Caddy")
	if err := stepInstallCaddy(s); err != nil {
		return err
	}

	PrintStep(7, 12, "Caddyfile")
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

	PrintStep(8, 12, "Скачать backend бинарь")
	localPath, err := stepDownloadAsset(dl, rel, "wg-monitor-backend-linux-amd64")
	if err != nil {
		return err
	}

	PrintStep(9, 12, "Upload + sha + swap")
	if err := stepUploadAndSwap(s, localPath, "/usr/local/bin/wg-monitor-backend", ""); err != nil {
		return err
	}

	PrintStep(10, 12, "Start service")
	if _, err := s.MustRun("systemctl start wg-monitor-backend"); err != nil {
		return err
	}
	PrintOK("wg-monitor-backend started")

	time.Sleep(3 * time.Second)

	PrintStep(11, 12, "Verify systemctl is-active")
	out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
	if strings.TrimSpace(out) != "active" {
		PrintFail("сервис не active. Логи:")
		jr, _ := s.MustRun("journalctl -u wg-monitor-backend -n 30 --no-pager")
		fmt.Println(jr)
		return fmt.Errorf("service not active")
	}
	PrintOK("active")

	PrintStep(12, 12, "Verify /health через домен")
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

	nick := Ask("Никнейм нового роутера (a-z0-9_-, уникальный)", "")
	if nick == "" {
		return fmt.Errorf("nickname required")
	}

	// Идемпотентность: если предыдущий прогон упал между «токен сгенерирован
	// + agent в wizard.toml» и «backend.yaml залит на VPS / install-agent
	// прошёл», nickname уже есть в state. В этом случае резюмируем — токен
	// в disk-кэше есть, threadID сохранён, нужно только дойти до конца.
	resume := false
	tokEnv := "WG_AGENT_TOKEN_" + strings.ToUpper(nick)
	if existing := state.FindAgent(nick); existing != nil {
		if secrets.GetNonInteractive(tokEnv) == "" {
			PrintFail("такой никнейм уже есть в wizard.toml, но токен утерян — почини вручную")
			return fmt.Errorf("duplicate nickname without cached token")
		}
		ans := strings.ToLower(strings.TrimSpace(Ask(
			fmt.Sprintf("nickname %q уже в wizard.toml — резюмировать установку? [y/N]", nick), "n")))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("aborted by user")
		}
		resume = true
		PrintOK(fmt.Sprintf("резюмирую установку %s (токен из disk-кэша, threadID=%d)", nick, existing.ThreadID))
	}

	// 1. Сгенерировать токен и сразу положить в disk-кэш секретов, чтобы
	// последующие действия (regen backend.yaml в этой сессии + любые
	// re-deploy в будущем) не просили его вводить вручную. На resume —
	// пропускаем, токен уже в disk-кэше.
	if !resume {
		tok := randomHexToken(32)
		if err := secrets.Set(tokEnv, tok); err != nil {
			PrintWarn("не смог сохранить токен в disk-кэш: " + err.Error())
		} else {
			PrintOK(fmt.Sprintf("токен %s сгенерирован и сохранён в disk-кэш", tokEnv))
		}
		// also seed the in-process env so old code paths reading via os.Getenv
		// (install-agent below, RenderBackendYAML loop) still find the value.
		os.Setenv(tokEnv, tok)
	}

	// 2. Добавить в backend.yaml на VPS.
	PrintStep(1, 3, "Обновить backend.yaml на VPS")
	pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
	if pass == "" {
		return fmt.Errorf("missing VPS password")
	}
	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	bs, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh, "backend")
	if err != nil {
		return err
	}
	defer bs.Close()

	// На resume — топик и запись в state уже есть, не дёргаем TG API повторно.
	if !resume {
		threadID := autoCreateForumTopic(state, secrets, nick)
		if threadID == 0 {
			threadID = parseIntOr(Ask("Telegram thread_id для нового топика", ""), 0)
		}
		state.Agents = append(state.Agents, AgentState{
			Nickname: nick,
			ThreadID: threadID,
		})
	}

	// Cross-PC fallback: токены агентов, установленных с другой машины, в
	// локальном disk-кэше отсутствуют (env+disk на этом ПК их никогда не
	// видели). Источник истины — задеплоенный backend.yaml на VPS, оттуда
	// и забираем недостающие. Соединение уже открыто (bs).
	deployedTokens := readDeployedAgentTokens(bs)

	// Все токены: для нового — только что сгенерированный (уже в disk-кэше),
	// для существующих — env → disk → deployed yaml. Если найден в env, но не
	// в disk-кэше — миграция: пишем в disk, чтобы следующий раз тоже нашёлся.
	var entries []AgentEntry
	for _, a := range state.Agents {
		envName := "WG_AGENT_TOKEN_" + strings.ToUpper(a.Nickname)
		t := secrets.GetNonInteractive(envName)
		if t == "" {
			if dt, ok := deployedTokens[a.Nickname]; ok && dt != "" {
				t = dt
				PrintInfo(fmt.Sprintf("токен для %s восстановлен из backend.yaml на VPS", a.Nickname))
			}
		}
		if t == "" {
			PrintFail(fmt.Sprintf(
				"токен для %s неизвестен. Если этот агент устанавливался раньше, добавь токен в %s или экспортируй %s",
				a.Nickname, secretsCachePath(), envName))
			return fmt.Errorf("missing token for %s", a.Nickname)
		}
		// best-effort migration into the disk cache; ignore write errors.
		_ = secrets.Set(envName, t)
		entries = append(entries, AgentEntry{
			Nickname: a.Nickname,
			Token:    t,
			ThreadID: a.ThreadID,
		})
	}

	// Bot token берём заново — т.к. не в state
	botToken, _ := secrets.Get("WG_BOT_TOKEN", "Telegram bot token", nil)
	yamlBytes, err := RenderBackendYAML(BackendParams{
		BotToken:    botToken,
		ChatID:      state.Telegram.ChatID,
		AdminUserID: state.Telegram.AdminUserID,
		Agents:      entries,
	})
	if err != nil {
		return err
	}
	if err := stepUploadFile(bs, "/etc/wg-monitor/backend.yaml", yamlBytes, "600"); err != nil {
		return err
	}

	PrintStep(2, 3, "Перезапустить бэкенд")
	if _, err := bs.MustRun("systemctl restart wg-monitor-backend"); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	out, _ := bs.MustRun("systemctl is-active wg-monitor-backend")
	if strings.TrimSpace(out) != "active" {
		jr, _ := bs.MustRun("journalctl -u wg-monitor-backend -n 30 --no-pager")
		PrintFail("бэкенд не active после restart:\n" + jr)
		return fmt.Errorf("backend not active")
	}
	PrintOK("бэкенд перезапущен")

	PrintStep(3, 3, "Установить агента на новый роутер")
	// Сохранить токен в env для текущего процесса, чтобы install-agent его подхватил.
	// Источник — disk-кэш: и в свежем прогоне (только что записан), и в resume.
	os.Setenv(tokEnv, secrets.GetNonInteractive(tokEnv))
	return actionInstallAgent(state, secrets, dl, nick)
}

// readDeployedAgentTokens fetches /etc/wg-monitor/backend.yaml from the VPS
// and returns nickname → token from its agents list. Used by actionAddRouter
// to recover tokens for agents originally enrolled from a different operator
// workstation: the secrets disk-cache is per-machine, but the deployed yaml
// is the source of truth on the server. All errors are warned but non-fatal
// — caller degrades to the existing "missing token" failure path.
func readDeployedAgentTokens(bs *SSH) map[string]string {
	out := map[string]string{}
	yamlOut, stderr, rc, err := bs.Run("cat /etc/wg-monitor/backend.yaml")
	if err != nil {
		PrintWarn("не смог прочитать backend.yaml с VPS (ssh transport): " + err.Error())
		return out
	}
	if rc != 0 {
		PrintWarn(fmt.Sprintf("cat /etc/wg-monitor/backend.yaml вернул rc=%d, stderr=%q", rc, strings.TrimSpace(stderr)))
		return out
	}
	if strings.TrimSpace(yamlOut) == "" {
		PrintWarn("backend.yaml на VPS пустой — нечего восстанавливать")
		return out
	}
	var by doctorBackendYAML
	if perr := yaml.Unmarshal([]byte(yamlOut), &by); perr != nil {
		PrintWarn("backend.yaml на VPS не парсится: " + perr.Error())
		return out
	}
	for _, a := range by.Agents {
		if a.Nickname != "" && a.Token != "" {
			out[a.Nickname] = a.Token
		}
	}
	if len(out) == 0 {
		PrintWarn(fmt.Sprintf("в backend.yaml на VPS нет агентов с токенами (parsed %d entries)", len(by.Agents)))
	} else {
		nicks := make([]string, 0, len(out))
		for k := range out {
			nicks = append(nicks, k)
		}
		PrintInfo(fmt.Sprintf("из backend.yaml на VPS поднято %d токен(ов): %s", len(out), strings.Join(nicks, ", ")))
	}
	return out
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

func actionStatus(state *State, secrets *SecretStore) error {
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		PrintFail("wizard.toml пустой — нечего проверять")
		return fmt.Errorf("nothing to check")
	}

	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")

	if state.Backend.Host != "" {
		fmt.Println(Colorize("=== Backend ===", ColorBold))
		pass, _ := secrets.Get("WG_VPS_PASS", "VPS root пароль", nil)
		if pass == "" {
			PrintWarn("WG_VPS_PASS не задан — пропускаю VPS")
		} else {
			s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh, "backend")
			if err != nil {
				PrintFail(err.Error())
			} else {
				out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
				PrintInfo("systemctl: " + strings.TrimSpace(out))
				if state.Backend.Domain != "" {
					stepVerifyHTTP(s, "https://"+state.Backend.Domain+"/healthz")
				}
				vout, _ := s.MustRun("/usr/local/bin/wg-monitor-backend --version 2>&1 || true")
				PrintInfo("version: " + strings.TrimSpace(vout))
				s.Close()
			}
		}
		fmt.Println()
	}

	for _, ag := range state.Agents {
		fmt.Println(Colorize("=== Agent: "+ag.Nickname+" ===", ColorBold))
		envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
		home, _ := os.UserHomeDir()
		memFile := filepath.Join(home, ".claude/projects/c--Users-user-Projects-wg-monitor/memory/host_keenetic.md")
		pass, _ := secrets.Get(envName, "пароль root для "+ag.Nickname, &MemoryFileLookup{
			Path:    memFile,
			Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
		})
		if pass == "" {
			pass, _ = secrets.Get("WG_KEENETIC_PASS", "пароль root", nil)
		}
		if pass == "" {
			PrintWarn("пароль не задан — пропускаю")
			continue
		}

		s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh, ag.Nickname)
		if err != nil {
			PrintFail(err.Error())
			continue
		}
		out, _ := s.MustRun("pidof wg-monitor")
		if strings.TrimSpace(out) != "" {
			PrintOK("running (PID " + strings.TrimSpace(out) + ")")
		} else {
			PrintFail("не запущен")
		}
		vout, _ := s.MustRun("/opt/bin/wg-monitor --version 2>&1 || true")
		PrintInfo("version: " + strings.TrimSpace(vout))
		s.Close()
		fmt.Println()
	}
	return nil
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
