package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// actionSmokeTest runs a battery of read-only checks against the deployed
// infrastructure (backend + every agent in wizard.toml) and prints a
// colored summary. Best-effort: a single failure is marked red but never
// aborts the whole run.
//
// The function MUST NOT prompt for anything mid-run — all secrets are
// resolved up front. Sections without credentials are skipped with a
// PrintWarn rather than blocking on a password prompt.
func actionSmokeTest(state *State, secrets *SecretStore) error {
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		PrintFail("wizard.toml пуст — нечего проверять")
		return fmt.Errorf("nothing to check")
	}

	var ok, total int
	add := func(passed bool) {
		total++
		if passed {
			ok++
		}
	}

	kh, _ := NewKnownHosts(defaultCacheDir() + "/known_hosts")

	// ---------- Backend ----------
	if state.Backend.Host != "" {
		fmt.Println(Colorize("=== Backend ===", ColorBold))

		// 1. HTTPS /healthz (no SSH required).
		if state.Backend.Domain == "" {
			PrintWarn("домен бэкенда не задан — пропускаю /healthz")
		} else {
			if smokeCheckHealthz("https://" + state.Backend.Domain + "/healthz") {
				add(true)
			} else {
				add(false)
			}
		}

		// 2. SSH-based checks (need VPS root password).
		pass := secrets.GetNonInteractive("WG_VPS_PASS")
		if pass == "" {
			PrintWarn("пропускаю VPS — пароль не задан (WG_VPS_PASS)")
		} else {
			port := state.Backend.Port
			if port == 0 {
				port = 22
			}
			user := state.Backend.User
			if user == "" {
				user = "root"
			}
			s, err := ConnectSSH(state.Backend.Host, port, user, pass, kh, "backend")
			if err != nil {
				PrintFail("SSH к VPS: " + err.Error())
				add(false)
			} else {
				// systemctl is-active
				out, _ := s.MustRun("systemctl is-active wg-monitor-backend")
				active := strings.TrimSpace(out)
				if active == "active" {
					PrintOK("systemctl is-active = active")
					add(true)
				} else {
					PrintFail("systemctl is-active = " + active)
					add(false)
				}

				// version (informational only)
				vout, _ := s.MustRun("/usr/local/bin/wg-monitor-backend -version 2>&1 || /usr/local/bin/wg-monitor-backend --version 2>&1 || true")
				PrintInfo("backend version: " + strings.TrimSpace(vout))

				// users DB ↔ wizard.toml agent reconciliation. The DB
				// (state.db / users table, managed by wg-monitor-cli) is
				// the source of truth; backend.yaml has no agents section.
				dbOut, _, rc, err := s.Run(`sqlite3 /var/lib/wg-monitor/state.db "SELECT nickname FROM users;"`)
				if err != nil || rc != 0 {
					PrintFail("read users from /var/lib/wg-monitor/state.db (rc=" + fmt.Sprint(rc) + "): " + fmt.Sprint(err))
					add(false)
				} else {
					missing := smokeCheckAgentReconcile(dbOut, state.Agents)
					if len(missing) == 0 {
						PrintOK("agents в wizard.toml совпадают с users в DB")
						add(true)
					} else {
						for _, n := range missing {
							PrintFail("agent " + n + " в wizard.toml но не в users DB на VPS")
						}
						add(false)
					}
				}
				s.Close()
			}
		}
		fmt.Println()
	}

	// ---------- Agents ----------
	for _, ag := range state.Agents {
		fmt.Println(Colorize("=== Agent: "+ag.Nickname+" ===", ColorBold))

		// 1. TCP probe (no creds needed).
		port := portOrDefault(ag.Port, 222)
		if probeReachable(ag.Host, port, 3*time.Second) {
			PrintOK(fmt.Sprintf("TCP %s:%d reachable", ag.Host, port))
			add(true)
		} else {
			PrintFail(fmt.Sprintf("TCP %s:%d не отвечает за 3с — VPN/SSTP не активен?", ag.Host, port))
			add(false)
			fmt.Println()
			continue
		}

		// 2. SSH-based checks.
		envName := "WG_KEENETIC_PASS_" + strings.ToUpper(ag.Nickname)
		pass := secrets.GetNonInteractive(envName)
		if pass == "" {
			pass = secrets.GetNonInteractive("WG_KEENETIC_PASS")
		}
		if pass == "" {
			// memory-file fallback (read-only, no prompt)
			home, _ := os.UserHomeDir()
			memFile := filepath.Join(home, ".claude/projects/c--Users-Anex-Projects-wg-monitor/memory/host_keenetic.md")
			if v, ok := lookupMemoryFile(memFile, `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`); ok {
				pass = v
			}
		}
		if pass == "" {
			PrintWarn("пропускаю агента — пароль не задан (" + envName + " или WG_KEENETIC_PASS)")
			fmt.Println()
			continue
		}

		s, err := ConnectSSH(ag.Host, port, userOrDefault(ag.User, "root"), pass, kh, ag.Nickname)
		if err != nil {
			PrintFail("SSH: " + err.Error())
			add(false)
			fmt.Println()
			continue
		}

		// pidof wg-monitor
		out, _ := s.MustRun("pidof wg-monitor")
		if strings.TrimSpace(out) != "" {
			PrintOK("running (PID " + strings.TrimSpace(out) + ")")
			add(true)
		} else {
			PrintFail("процесс wg-monitor не запущен")
			add(false)
		}

		// installed version
		vout, _ := s.MustRun("cat /opt/etc/wg-monitor/state-version 2>/dev/null || /opt/bin/wg-monitor -version 2>/dev/null || /opt/bin/wg-monitor --version 2>/dev/null || true")
		installed := strings.TrimSpace(vout)
		if installed == "" {
			PrintInfo("installed version: (не определена)")
		} else {
			PrintInfo("installed version: " + installed)
		}
		if installed != "" && ag.LastDeployedVersion != "" && !strings.Contains(installed, ag.LastDeployedVersion) {
			PrintWarn(fmt.Sprintf("installed=%s but state says %s", installed, ag.LastDeployedVersion))
		}

		s.Close()
		fmt.Println()
	}

	// ---------- Summary ----------
	fmt.Println(Colorize(fmt.Sprintf("%d/%d проверок прошли", ok, total), ColorBold))
	return nil
}

// smokeCheckHealthz hits the URL with a 5s timeout; expects 2xx and prints
// a coloured line. Returns true on success.
func smokeCheckHealthz(url string) bool {
	cli := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "wg-monitor-deploy/smoke")
	resp, err := cli.Do(req)
	if err != nil {
		PrintFail(url + ": " + err.Error())
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		PrintOK(fmt.Sprintf("%s → HTTP %d (%s)", url, resp.StatusCode, strings.TrimSpace(string(body))))
		return true
	}
	PrintFail(fmt.Sprintf("%s → HTTP %d", url, resp.StatusCode))
	return false
}

// smokeCheckAgentReconcile returns nicknames present in wizard.toml's
// agents list that are missing from the deployed users table on the VPS.
// `dbNicknamesOutput` is sqlite3's plain-text output of `SELECT nickname FROM users` —
// one nickname per line.
func smokeCheckAgentReconcile(dbNicknamesOutput string, agents []AgentState) []string {
	deployed := map[string]bool{}
	for _, line := range strings.Split(dbNicknamesOutput, "\n") {
		if n := strings.TrimSpace(line); n != "" {
			deployed[n] = true
		}
	}
	var missing []string
	for _, a := range agents {
		if !deployed[a.Nickname] {
			missing = append(missing, a.Nickname)
		}
	}
	return missing
}
