package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// actionUpdateComponents replaces the old "[2] Обновить бэкенд" and
// "[4] Обновить агента" menu items with one flow:
//
//  1. Probe the latest GitHub release.
//  2. Show a per-component status table (backend + every agent), marking
//     outdated rows.
//  3. Ask which to update.
//  4. For each chosen target, render an explicit context block (alias,
//     address, last deploy, current public IP of the operator — to spot
//     "wrong SSTP active") and require an explicit y/N before any SSH.
//  5. TCP-probe the target before SSH and abort early when unreachable
//     ("VPN не подключён?").
//  6. Delegate to the existing actionUpdateBackend / actionUpdateAgent.
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

	targets := buildUpdateTargets(state, rel.TagName)
	printUpdateStatusTable(targets)

	outdated := filterOutdated(targets)
	if len(outdated) == 0 {
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
		if t.IsAgent {
			if !probeReachable(t.Host, t.Port, 3*time.Second) {
				PrintFail(fmt.Sprintf("%s:%d не отвечает за 3с — проверь SSTP/VPN, либо вручную исправь host/port в wizard.toml", t.Host, t.Port))
				continue
			}
		}
		if err := runOneUpdate(state, secrets, dl, t); err != nil {
			PrintFail(t.Label + ": " + err.Error())
		}
	}
	return nil
}

// updateTarget describes one row in the update table and is consumed by the
// runner to dispatch the right install action.
type updateTarget struct {
	Label             string // e.g. "backend (vps.example.com)" or "agent testkeen"
	IsAgent           bool   // false → backend, true → an agent
	AgentNickname     string // populated when IsAgent
	Host              string
	Port              int
	InstalledVersion  string // last deployed version recorded in wizard.toml
	LatestVersion     string // GitHub latest tag
	LastDeploy        string
	NeedsUpdate       bool
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
			InstalledVersion: state.Backend.LastDeployedVersion,
			LatestVersion:    latest,
			LastDeploy:       state.Backend.LastDeploy,
			NeedsUpdate:      state.Backend.LastDeployedVersion != latest,
		})
	}
	for _, a := range state.Agents {
		out = append(out, updateTarget{
			Label:            "agent " + a.Nickname,
			IsAgent:          true,
			AgentNickname:    a.Nickname,
			Host:             a.Host,
			Port:             portOrDefault(a.Port, 222),
			InstalledVersion: a.LastDeployedVersion,
			LatestVersion:    latest,
			LastDeploy:       a.LastDeploy,
			NeedsUpdate:      a.LastDeployedVersion != latest,
		})
	}
	return out
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

func printUpdateStatusTable(rows []updateTarget) {
	fmt.Println(Colorize("Состояние компонентов:", ColorBold))
	for _, t := range rows {
		installed := t.InstalledVersion
		if installed == "" {
			installed = "(неизвестно)"
		}
		var status string
		switch {
		case t.NeedsUpdate && t.InstalledVersion == "":
			status = Colorize("[версия неизвестна]", ColorYellow)
		case t.NeedsUpdate:
			status = Colorize("[обновить → "+t.LatestVersion+"]", ColorYellow)
		default:
			status = Colorize("[актуально]", ColorGreen)
		}
		fmt.Printf("  • %-30s %-15s %s\n", t.Label, installed, status)
	}
	fmt.Println()
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
		fmt.Printf("  [%s] %s\n", o.key, o.t.Label)
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

// confirmTargetContext renders a one-screen summary of WHERE we're about to
// SSH and WHAT we'll change, then asks for explicit y/N. The aim is to make
// "забыл переключить SSTP, сейчас засунем агента клиента А на роутер
// клиента Б" require typing 'y' to happen.
func confirmTargetContext(t updateTarget, currentIP string) bool {
	fmt.Println(Colorize("─────────────────────────────────────────────", ColorCyan))
	fmt.Printf("%s %s\n", Colorize("Цель:", ColorBold), t.Label)
	fmt.Printf("  Адрес       : %s:%d\n", t.Host, t.Port)
	if t.IsAgent {
		fmt.Println("  Сеть        : LAN (требуется активный SSTP/VPN до клиента)")
	} else {
		fmt.Println("  Сеть        : Интернет (публичный VPS)")
	}
	fmt.Printf("  Установлено : %s\n", nonEmpty(t.InstalledVersion, "(неизвестно)"))
	fmt.Printf("  Будет       : %s\n", t.LatestVersion)
	fmt.Printf("  Last deploy : %s\n", nonEmpty(t.LastDeploy, "—"))
	if currentIP != "" {
		fmt.Printf("  Твой IP     : %s\n", currentIP)
	} else {
		fmt.Printf("  Твой IP     : %s\n", Colorize("не определён", ColorDim))
	}
	fmt.Println(Colorize("─────────────────────────────────────────────", ColorCyan))
	resp := strings.ToLower(strings.TrimSpace(Ask("Подтвердить и продолжить? [y/N]", "")))
	return resp == "y" || resp == "yes" || resp == "д" || resp == "да"
}

// runOneUpdate dispatches the right per-component install action.
func runOneUpdate(state *State, secrets *SecretStore, dl *Downloader, t updateTarget) error {
	if t.IsAgent {
		return actionUpdateAgent(state, secrets, dl, t.AgentNickname)
	}
	return actionUpdateBackend(state, secrets, dl)
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
