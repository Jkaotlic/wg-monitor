package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// probeAgentTokenValid GETs /v1/cmd with the supplied raw token and reports
// whether the response status is anything other than 401 — that's the only
// signal that confirms the disk-cached token still matches the SHA-256 hash
// stored in the users-table on the VPS. Used by doctorAgent for the auth-probe
// check (a green doctor without this is a false positive — see DOC-01).
func probeAgentTokenValid(url, rawToken string) bool {
	cli := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+rawToken)
	req.Header.Set("User-Agent", "wg-monitor-deploy/auth-probe")
	resp, err := cli.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode != http.StatusUnauthorized
}

// doctorTally is the running counter the per-section helpers feed into.
// At the end of actionDoctor it's painted as a colored summary line.
type doctorTally struct {
	pass int
	warn int
	fail int
}

func (t *doctorTally) ok(msg string)    { t.pass++; PrintOK(msg) }
func (t *doctorTally) warnf(msg string) { t.warn++; PrintWarn(msg) }
func (t *doctorTally) failf(msg string) { t.fail++; PrintFail(msg) }

type doctorOptions struct {
	Deep bool
}

// doctorBackendYAML matches just the bits we need to validate from
// /etc/wg-monitor/backend.yaml on the VPS. Agent enrollment is NOT in this
// file — it lives in /var/lib/wg-monitor/state.db's users table — so we
// only check that the loader-required fields are present.
type doctorBackendYAML struct {
	Telegram struct {
		BotTokenFile string `yaml:"bot_token_file"`
		ChatID       int64  `yaml:"chat_id"`
		AdminUserID  int64  `yaml:"admin_user_id"`
	} `yaml:"telegram"`
}

type doctorTunnelsEnvelope struct {
	Success bool `json:"success"`
	Data    struct {
		Tunnels []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Status        string `json:"status"`
			Enabled       bool   `json:"enabled"`
			DefaultRoute  bool   `json:"defaultRoute"`
			InterfaceName string `json:"interfaceName"`
		} `json:"tunnels"`
	} `json:"data"`
}

// dialableTCP is a private clone of probeReachable from update_components.go.
// Inlined to keep doctor.go independent of files owned by parallel work.
func dialableTCP(host string, port int, timeout time.Duration) bool {
	if host == "" || port == 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func actionDoctor(state *State, secrets *SecretStore, opts doctorOptions) error {
	tally := &doctorTally{}

	// ----- Local state -----
	doctorLocalState(state, secrets, tally)

	// ----- VPS / backend -----
	doctorBackend(state, secrets, tally)

	// ----- Agents -----
	for i := range state.Agents {
		doctorAgent(state, &state.Agents[i], secrets, tally, opts)
	}

	// Summary.
	fmt.Println()
	summary := fmt.Sprintf("%d passed, %d warnings, %d failed", tally.pass, tally.warn, tally.fail)
	switch {
	case tally.fail > 0:
		fmt.Println(Colorize(summary, ColorRed))
	case tally.warn > 0:
		fmt.Println(Colorize(summary, ColorYellow))
	default:
		fmt.Println(Colorize(summary, ColorGreen))
	}
	if tally.fail > 0 {
		return fmt.Errorf("doctor: %d checks failed", tally.fail)
	}
	return nil
}

func doctorLocalState(state *State, secrets *SecretStore, t *doctorTally) {
	fmt.Println(Colorize("=== Local state ===", ColorBold))

	// wizard.toml parsed (LoadState returned a non-nil State, so just check
	// the agent list non-emptiness as a proxy for usefulness).
	if len(state.Agents) == 0 {
		t.warnf("wizard.toml: agent list пуст")
	} else {
		t.ok(fmt.Sprintf("wizard.toml: %d агент(а)", len(state.Agents)))
	}

	// secrets.env presence + per-agent token coverage.
	secPath := secretsCachePath()
	if secPath == "" {
		t.warnf("WG_NO_SECRET_CACHE=1 — disk-кэш секретов отключён, пропускаю проверку токенов")
	} else if _, err := os.Stat(secPath); err != nil {
		if os.IsNotExist(err) {
			t.warnf("secrets.env не существует (" + secPath + ")")
		} else {
			t.failf("stat " + secPath + ": " + err.Error())
		}
	} else {
		t.ok("secrets.env найден: " + secPath)
		for _, a := range state.Agents {
			env := "WG_AGENT_TOKEN_" + strings.ToUpper(a.Nickname)
			if v := secrets.GetNonInteractive(env); v == "" {
				t.failf(env + " отсутствует в env/disk")
			} else {
				t.ok(env + " присутствует")
			}
		}
	}

	doctorSharedPrivateHostHints(state, t)

	// known_hosts: aliases per agent + backend.
	khPath := filepath.Join(defaultCacheDir(), "known_hosts")
	aliases, err := ListKnownHostAliases(khPath)
	if err != nil {
		t.failf("known_hosts read: " + err.Error())
	} else if len(aliases) == 0 {
		t.warnf("known_hosts пуст или отсутствует (" + khPath + ")")
	} else {
		t.ok(fmt.Sprintf("known_hosts: %d записей", len(aliases)))
		need := []string{}
		if state.Backend.Host != "" {
			need = append(need, "backend")
		}
		for _, a := range state.Agents {
			need = append(need, a.Nickname)
		}
		have := map[string]bool{}
		for _, a := range aliases {
			have[a] = true
		}
		for _, n := range need {
			if have[n] {
				t.ok("known_hosts has alias " + n)
			} else {
				t.warnf("known_hosts: нет записи для " + n + " (TOFU при первом подключении создаст)")
			}
		}
	}
	fmt.Println()
}

func doctorSharedPrivateHostHints(state *State, t *doctorTally) {
	byHost := make(map[string][]string)
	for _, a := range state.Agents {
		ip := net.ParseIP(a.Host)
		if ip == nil || !ip.IsPrivate() {
			continue
		}
		byHost[a.Host] = append(byHost[a.Host], a.Nickname)
	}
	for host, names := range byHost {
		if len(names) < 2 {
			continue
		}
		t.warnf(fmt.Sprintf(
			"несколько агентов используют приватный host %s (%s): TCP doctor зависит от текущего VPN/маршрута; для install/update wizard дополнительно делает path discovery",
			host, strings.Join(names, ", "),
		))
	}
}

func doctorBackend(state *State, secrets *SecretStore, t *doctorTally) {
	fmt.Println(Colorize("=== Backend (VPS) ===", ColorBold))
	if state.Backend.Host == "" {
		t.warnf("в wizard.toml нет [backend] — пропускаю секцию")
		fmt.Println()
		return
	}

	auth, err := backendSSHAuthMethodsNonInteractive(state, secrets)
	if err != nil {
		t.warnf("VPS SSH credential неизвестен (" + err.Error() + ") — пропускаю SSH-проверки")
		fmt.Println()
		return
	}

	port := state.Backend.Port
	if port == 0 {
		port = 22
	}
	user := state.Backend.User
	if user == "" {
		user = "root"
	}

	if !dialableTCP(state.Backend.Host, port, 3*time.Second) {
		t.failf(fmt.Sprintf("TCP %s:%d не отвечает за 3с", state.Backend.Host, port))
		fmt.Println()
		return
	}
	t.ok(fmt.Sprintf("TCP %s:%d — reachable", state.Backend.Host, port))

	kh, err := NewKnownHosts(filepath.Join(defaultCacheDir(), "known_hosts"))
	if err != nil {
		t.failf("known_hosts open: " + err.Error())
		fmt.Println()
		return
	}
	s, err := ConnectSSHWithAuth(state.Backend.Host, port, user, auth, kh, "backend")
	if err != nil {
		t.failf("SSH к VPS: " + err.Error())
		fmt.Println()
		return
	}
	defer s.Close()
	t.ok("SSH к VPS установлен")

	// systemctl is-active
	if out, _, _, err := s.Run("systemctl is-active wg-monitor-backend"); err == nil {
		svcState := strings.TrimSpace(out)
		if svcState == "active" {
			t.ok("systemctl is-active: active")
		} else {
			t.failf("systemctl is-active: " + svcState)
		}
	} else {
		t.failf("systemctl is-active failed: " + err.Error())
	}

	// /healthz
	if state.Backend.Domain != "" {
		url := "https://" + state.Backend.Domain + "/healthz"
		out, _, _, err := s.Run(fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' %s", url))
		if err != nil {
			t.failf(url + ": " + err.Error())
		} else if code := strings.TrimSpace(out); code == "200" {
			t.ok(url + " -> 200")
		} else {
			t.failf(url + " -> HTTP " + code)
		}

		readyURL := "https://" + state.Backend.Domain + "/readyz"
		out, _, _, err = s.Run(fmt.Sprintf("curl -sS -o /dev/null -w '%%{http_code}' %s", readyURL))
		if err != nil {
			t.failf(readyURL + ": " + err.Error())
		} else if code := strings.TrimSpace(out); code == "200" {
			t.ok(readyURL + " -> 200")
		} else {
			t.failf(readyURL + " -> HTTP " + code)
		}
	} else {
		t.warnf("backend.domain не задан в wizard.toml — пропускаю /healthz")
	}

	// backend.yaml parses + has loader-required fields. Agents/users
	// coverage is checked separately against the DB below.
	yamlOut, _, rc, err := s.Run("cat /etc/wg-monitor/backend.yaml")
	if err != nil || rc != 0 {
		t.failf("чтение /etc/wg-monitor/backend.yaml не удалось")
	} else {
		var by doctorBackendYAML
		if perr := yaml.Unmarshal([]byte(yamlOut), &by); perr != nil {
			t.failf("backend.yaml не парсится: " + perr.Error())
		} else {
			t.ok("backend.yaml парсится")
			if by.Telegram.BotTokenFile == "" {
				t.failf("backend.yaml: telegram.bot_token_file пустое (config-loader откажется стартовать)")
			}
			if by.Telegram.ChatID == 0 {
				t.failf("backend.yaml: telegram.chat_id == 0")
			}
			if by.Telegram.AdminUserID == 0 {
				t.failf("backend.yaml: telegram.admin_user_id == 0")
			}
			if by.Telegram.BotTokenFile != "" {
				if out, _, rc, _ := s.Run("test -f " + shellSingleQuote(by.Telegram.BotTokenFile)); rc != 0 {
					t.failf("backend.yaml ссылается на " + by.Telegram.BotTokenFile + ", но файла нет на VPS: " + strings.TrimSpace(out))
				} else {
					t.ok(by.Telegram.BotTokenFile + " существует")
				}
			}
		}
	}

	// users DB ↔ wizard.toml agents reconciliation. The DB is the source
	// of truth — populated by wg-monitor-cli add-user (called from the
	// wizard's [3] Add/install Router action).
	dbOut, _, rc2, err2 := s.Run(`sqlite3 /var/lib/wg-monitor/state.db "SELECT nickname || '|' || COALESCE(strftime('%Y-%m-%dT%H:%M:%SZ', last_seen_at), '') FROM users;"`)
	if err2 != nil || rc2 != 0 {
		t.failf("чтение users из /var/lib/wg-monitor/state.db не удалось")
	} else {
		have := map[string]bool{}
		lastSeen := map[string]string{}
		for _, line := range strings.Split(dbOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			n, seen, _ := strings.Cut(line, "|")
			if n = strings.TrimSpace(n); n != "" {
				have[n] = true
				lastSeen[n] = strings.TrimSpace(seen)
			}
		}
		t.ok(fmt.Sprintf("users в DB: %d", len(have)))
		for _, ag := range state.Agents {
			if have[ag.Nickname] {
				hb := doctorFormatLastSeen(lastSeen[ag.Nickname])
				if strings.HasPrefix(hb, "fresh") {
					t.ok("DB last_seen " + ag.Nickname + ": " + hb)
				} else {
					t.warnf("DB last_seen " + ag.Nickname + ": " + hb)
				}
				t.ok("DB: agent " + ag.Nickname + " присутствует")
			} else {
				t.failf("DB: НЕТ записи для агента " + ag.Nickname + " (запусти [3] Добавить/установить роутер)")
			}
		}
	}

	// Disk free on /
	if out, _, rc, err := s.Run("df -B1 / | awk 'NR==2 {print $4}'"); err == nil && rc == 0 {
		var free int64
		fmt.Sscanf(strings.TrimSpace(out), "%d", &free)
		const oneGiB = int64(1) << 30
		if free > oneGiB {
			t.ok(fmt.Sprintf("disk free /: %.2f GiB", float64(free)/float64(oneGiB)))
		} else {
			t.failf(fmt.Sprintf("disk free / < 1 GiB (%.2f GiB)", float64(free)/float64(oneGiB)))
		}
	} else {
		t.warnf("df / не отработал")
	}

	// DB file size
	if out, _, rc, err := s.Run("stat -c %s /var/lib/wg-monitor/state.db 2>/dev/null"); err == nil && rc == 0 && strings.TrimSpace(out) != "" {
		t.ok("state.db size: " + strings.TrimSpace(out) + " bytes")
	} else {
		t.warnf("state.db не найден или stat не сработал")
	}

	fmt.Println()
}

func doctorAgent(state *State, ag *AgentState, secrets *SecretStore, t *doctorTally, opts doctorOptions) {
	fmt.Println(Colorize("=== Agent: "+ag.Nickname+" ===", ColorBold))

	port := portOrDefault(ag.Port, 222)
	user := userOrDefault(ag.User, "root")
	doctorAgentHeartbeat(state, ag, secrets, t)

	// Auth-probe: проверяем что raw-токен в disk-кэше всё ещё совпадает с
	// token_hash в DB — единственный способ убедиться, что агент сможет
	// аутентифицироваться. "doctor зелёный" без этой проверки = ложное
	// успокоение.
	if state.Backend.Domain != "" {
		tok := secrets.GetNonInteractive("WG_AGENT_TOKEN_" + strings.ToUpper(ag.Nickname))
		switch {
		case tok == "":
			t.warnf("auth-probe пропущен: токен не в disk-кэше (запусти [3] на этом ПК или импортируй secrets backup)")
		case probeAgentTokenValid("https://"+state.Backend.Domain+"/v1/cmd?wait=0", tok):
			t.ok("auth-probe: токен валиден")
		default:
			t.failf("auth-probe: backend ответил 401 — disk-cache токен НЕ совпадает с token_hash в DB")
		}
	}

	// TCP probe (3s)
	if !doctorAgentReachable(ag, port, t, opts) {
		t.failf(fmt.Sprintf("TCP %s:%d не отвечает за 3с (VPN отключён?)", ag.Host, port))
		fmt.Println()
		return
	}
	t.ok(fmt.Sprintf("TCP %s:%d — reachable", ag.Host, port))

	// Resolve password non-interactively (env or disk).
	envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
	pass := secrets.GetNonInteractive(envName)
	if pass == "" {
		pass = secrets.GetNonInteractive("WG_KEENETIC_PASS")
	}
	if pass == "" {
		t.warnf("пароль для " + ag.Nickname + " неизвестен (env/disk пусты) — пропускаю SSH")
		fmt.Println()
		return
	}

	kh, err := NewKnownHosts(filepath.Join(defaultCacheDir(), "known_hosts"))
	if err != nil {
		t.failf("known_hosts open: " + err.Error())
		fmt.Println()
		return
	}
	s, err := ConnectSSH(ag.Host, port, user, pass, kh, ag.Nickname)
	if err != nil {
		t.failf("SSH к " + ag.Nickname + ": " + err.Error())
		fmt.Println()
		return
	}
	defer s.Close()
	t.ok("SSH к " + ag.Nickname + " OK")

	// pidof wg-monitor
	if out, _, _, err := s.Run("pidof wg-monitor"); err == nil && strings.TrimSpace(out) != "" {
		t.ok("wg-monitor running (PID " + strings.TrimSpace(out) + ")")
	} else {
		t.failf("wg-monitor не запущен (pidof пуст)")
	}

	// Installed version vs LastDeployedVersion.
	out, _, _, _ := s.Run("/opt/bin/wg-monitor -version 2>&1 | head -1; /opt/bin/wg-monitor --version 2>&1 | head -1")
	installed := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if installed == "" {
		t.warnf("установленная версия не определилась")
	} else if ag.LastDeployedVersion == "" {
		t.warnf("в wizard.toml нет last_deployed_version — нечем сравнить (running: " + installed + ")")
	} else if strings.Contains(installed, ag.LastDeployedVersion) {
		t.ok("версия совпадает с wizard.toml: " + installed)
	} else {
		t.warnf(fmt.Sprintf("версия расходится: installed=%q, wizard.toml=%q", installed, ag.LastDeployedVersion))
	}

	doctorAwgManagerViaSSH(s, t)
	doctorAgentLogsViaSSH(s, t)

	fmt.Println()
}

func doctorAgentReachable(ag *AgentState, port int, t *doctorTally, opts doctorOptions) bool {
	if !opts.Deep {
		return dialableTCP(ag.Host, port, 3*time.Second)
	}

	target := fmt.Sprintf("%s:%d", ag.Host, port)
	PrintInfo("deep path discovery: probing " + target)
	rep, cleanup, err := stepFindReachablePath(NewRealProber(), target, 5*time.Second)
	defer cleanup()
	if err != nil {
		t.warnf("deep path discovery: " + err.Error())
		return false
	}
	fmt.Print(describePath(rep))
	if rep.Chosen == nil {
		t.warnf("deep path discovery: no responding route")
		return false
	}
	PrintOK(fmt.Sprintf("deep path discovery: selected %s", rep.Chosen.Iface))
	return true
}

func doctorAgentHeartbeat(state *State, ag *AgentState, secrets *SecretStore, t *doctorTally) {
	if state.Backend.Domain == "" {
		t.warnf("heartbeat: backend.domain не задан — пропускаю last_seen")
		return
	}
	tok := secrets.GetNonInteractive("WIZARD_TOKEN")
	if tok == "" {
		t.warnf("heartbeat: WIZARD_TOKEN не найден в env/disk — пропускаю last_seen")
		return
	}
	c := NewVPSClient(state.Backend.Domain, tok)
	if c == nil {
		t.warnf("heartbeat: wizard client не собрался")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	hb := c.HeartbeatStatus(ctx, ag.Nickname)
	cancel()
	switch {
	case hb == "":
		t.warnf("heartbeat: не удалось прочитать /v1/wizard/agents")
	case strings.HasPrefix(hb, "fresh"):
		t.ok("heartbeat: " + hb)
	case hb == "never":
		t.warnf("heartbeat: never")
	default:
		t.warnf("heartbeat: " + hb)
	}
}

func doctorAwgManagerViaSSH(s *SSH, t *doctorTally) {
	if out, _, rc, err := s.Run("/opt/etc/init.d/S99awg-manager status 2>&1"); err != nil {
		t.warnf("S99awg-manager status: " + err.Error())
	} else if rc == 0 {
		t.ok("S99awg-manager status: " + doctorOneLine(out))
	} else {
		t.warnf("S99awg-manager status rc=" + fmt.Sprint(rc) + ": " + doctorOneLine(out))
	}

	_, code, rc, err := doctorCurlBodyCode(s, "http://127.0.0.1:2222/api/health", 3*time.Second)
	if err != nil || rc != 0 {
		t.failf("awg-manager /api/health: curl failed")
	} else if code == "200" {
		t.ok("awg-manager /api/health -> 200")
	} else {
		t.warnf("awg-manager /api/health -> HTTP " + code)
	}

	_, code, rc, err = doctorCurlBodyCode(s, "http://127.0.0.1:2222/api/system/info", 3*time.Second)
	if err != nil || rc != 0 {
		t.warnf("awg-manager /api/system/info: curl failed")
	} else if code == "200" {
		t.ok("awg-manager /api/system/info -> 200")
	} else if code == "401" || code == "403" {
		t.warnf("awg-manager /api/system/info -> HTTP " + code + " (API protected by auth)")
	} else {
		t.warnf("awg-manager /api/system/info -> HTTP " + code)
	}

	body, code, rc, err := doctorCurlBodyCode(s, "http://127.0.0.1:2222/api/tunnels/all", 5*time.Second)
	if err != nil || rc != 0 {
		t.warnf("awg-manager /api/tunnels/all: curl failed")
		return
	}
	if code == "401" || code == "403" {
		t.warnf("awg-manager /api/tunnels/all -> HTTP " + code + " (API protected by auth)")
		return
	}
	if code != "200" {
		t.warnf("awg-manager /api/tunnels/all -> HTTP " + code)
		return
	}
	var env doctorTunnelsEnvelope
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.warnf("awg-manager /api/tunnels/all JSON parse: " + err.Error())
		return
	}
	running := 0
	var defaults []string
	for _, tun := range env.Data.Tunnels {
		if tun.Status == "running" {
			running++
		}
		if tun.DefaultRoute {
			name := tun.Name
			if name == "" {
				name = tun.ID
			}
			if tun.InterfaceName != "" {
				name += " (" + tun.InterfaceName + ")"
			}
			defaults = append(defaults, name)
		}
	}
	msg := fmt.Sprintf("awg-manager tunnels: %d/%d running", running, len(env.Data.Tunnels))
	if len(defaults) > 0 {
		msg += "; default " + strings.Join(defaults, ", ")
	}
	if len(env.Data.Tunnels) > 0 && running == 0 {
		t.failf(msg)
	} else {
		t.ok(msg)
	}
}

func doctorAgentLogsViaSSH(s *SSH, t *doctorTally) {
	cmd := `(tail -n 80 /opt/var/log/wg-monitor.log 2>/dev/null; logread 2>/dev/null | grep -Ei 'wg-monitor|awg-manager' | tail -n 80) | tail -n 80`
	out, _, rc, err := s.Run(cmd)
	if err != nil || rc != 0 {
		t.warnf("logs: не удалось прочитать tail/logread")
		return
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		t.warnf("logs: нет строк по wg-monitor/awg-manager")
		return
	}
	recentBad := 0
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "fail") || strings.Contains(lower, "panic") {
			recentBad++
		}
	}
	last := doctorTrim(lines[len(lines)-1], 180)
	if recentBad > 0 {
		t.warnf(fmt.Sprintf("logs: %d recent error-like lines; last: %s", recentBad, last))
		return
	}
	t.ok(fmt.Sprintf("logs: %d recent lines, no error-like markers; last: %s", len(lines), last))
}

func doctorCurlBodyCode(s *SSH, url string, timeout time.Duration) (body, code string, rc int, err error) {
	sec := int(timeout.Seconds())
	if sec <= 0 {
		sec = 3
	}
	cmd := fmt.Sprintf(
		"curl -sS --max-time %d -H %s -w '\\n__HTTP_CODE__:%%{http_code}' %s",
		sec,
		shellSingleQuote("X-Requested-With: XMLHttpRequest"),
		shellSingleQuote(url),
	)
	out, _, rc, err := s.Run(cmd)
	marker := "\n__HTTP_CODE__:"
	idx := strings.LastIndex(out, marker)
	if idx < 0 {
		return out, "", rc, err
	}
	return out[:idx], strings.TrimSpace(out[idx+len(marker):]), rc, err
}

func doctorFormatLastSeen(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "never"
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "unparseable " + raw
	}
	return formatHeartbeatStatus(&ts, time.Now())
}

func doctorOneLine(s string) string {
	lines := nonEmptyLines(s)
	if len(lines) == 0 {
		return "(empty)"
	}
	return doctorTrim(lines[0], 180)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func doctorTrim(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
