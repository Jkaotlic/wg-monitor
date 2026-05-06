package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	s, err := ConnectSSH(state.Backend.Host, port, user, pass, kh)
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

	PrintStep(4, 4, "Verify /health")
	if state.Backend.Domain == "" {
		PrintWarn("домен не задан в wizard.toml — пропускаю /health проверку")
	} else {
		url := "https://" + state.Backend.Domain + "/health"
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
	s, err := ConnectSSH(ag.Host, portOrDefault(ag.Port, 222), userOrDefault(ag.User, "root"), pass, kh)
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
	s, err := ConnectSSH(ag.Host, ag.Port, ag.User, pass, kh)
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
	s, err := ConnectSSH(state.Backend.Host, state.Backend.Port, state.Backend.User, pass, kh)
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
	url := "https://" + state.Backend.Domain + "/health"
	if err := stepVerifyHTTP(s, url); err != nil {
		PrintWarn("health check не прошёл — возможно DNS ещё не прогрелся, проверь руками")
	}

	state.Backend.LastDeploy = time.Now().UTC().Format(time.RFC3339)
	state.Backend.LastDeployedVersion = rel.TagName
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
