package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func RunMenu(state *State, statePath string, secrets *SecretStore, dl *Downloader) {
	PrintBanner()

	// Best-effort: pull fresh fleet picture at startup so the menu reflects
	// what's actually on VPS. Silent on first-run / offline / missing token.
	if state.Backend.Domain != "" {
		if tok := secrets.GetNonInteractive("WIZARD_TOKEN"); tok != "" {
			if c := NewVPSClient(state.Backend.Domain, tok); c != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if remote, err := c.ListAgents(ctx); err == nil {
					merged, added, _ := MergeAgents(state.Agents, remote)
					state.Agents = merged
					if len(added) > 0 {
						PrintInfo(fmt.Sprintf("VPS sync на старте: добавлено %d новых роутеров (%s)", len(added), strings.Join(added, ", ")))
						_ = SaveState(statePath, state)
					}
				} else {
					PrintWarn("⚠ VPS unreachable на старте — работаю с локальным кэшем")
				}
				cancel()
			}
		}
	}

	for {
		printMenuHeader(state)
		printMenuItems(state)
		fmt.Print("> ")
		r := bufio.NewReader(os.Stdin)
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(strings.ToUpper(line))

		switch line {
		case "1":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallBackend(state, secrets, dl)
			})
		case "2":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateComponents(state, secrets, dl)
			})
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAddRouter(state, secrets, dl)
			})
		case "4":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUninstallAgent(state, secrets, askUninstallTarget(state))
			})
		case "5":
			actionDoctor(state, secrets, doctorOptions{}) //nolint:errcheck
		case "6":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionSyncVPS(state, secrets)
			})
		case "7":
			openInEditor(statePath)
			if reloaded, err := LoadState(statePath); err == nil {
				*state = *reloaded
			}
		case "8":
			ForgetKnownHostInteractive(state) //nolint:errcheck
		case "9":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionRepairAgentToken(state, secrets, dl, "")
			})
		case "10":
			if err := actionNetfix(state, netfixOptions{}); err != nil {
				PrintFail("netfix failed: " + err.Error())
			}
		case "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1–10 или Q.")
		}
		fmt.Println()
		Ask("[Enter] чтобы вернуться в меню", "")
	}
}

func printMenuHeader(state *State) {
	fmt.Println()
	fmt.Println(Colorize("╔═══════════════════════════════════════════════════════════╗", ColorCyan))
	fmt.Printf("%s wg-monitor deploy wizard         %-15s%s\n",
		Colorize("║", ColorCyan), Version, Colorize("║", ColorCyan))
	fmt.Println(Colorize("╠═══════════════════════════════════════════════════════════╣", ColorCyan))
	if state.Backend.Host != "" {
		fmt.Printf("%s VPS:    %s  %-30s%s\n",
			Colorize("║", ColorCyan), state.Backend.Host, state.Backend.Domain, Colorize("║", ColorCyan))
	}
	for _, a := range state.Agents {
		fmt.Printf("%s Router: %s:%d (%s)%-20s%s\n",
			Colorize("║", ColorCyan), a.Host, a.Port, a.Nickname, "", Colorize("║", ColorCyan))
	}
	fmt.Println(Colorize("╚═══════════════════════════════════════════════════════════╝", ColorCyan))
}

func printMenuItems(state *State) {
	fmt.Println()
	fmt.Println("  [1] Установить бэкенд          " + Colorize("(первичная установка на VPS)", ColorDim))
	if state.Backend.Host == "" && len(state.Agents) == 0 {
		fmt.Println("  [2] Обновить компоненты        " + Colorize("(сначала установи)", ColorDim))
	} else {
		fmt.Println("  [2] Обновить компоненты        " + Colorize("(проверка релиза + выбор что обновить)", ColorDim))
	}
	fmt.Println("  [3] Добавить/установить роутер " + Colorize("(DB + TG-топик + агент на роутер)", ColorDim))
	fmt.Println("  [4] Удалить агента             " + Colorize("(снести агент с роутера)", ColorDim))
	fmt.Println("  [5] Проверить состояние        " + Colorize("(Doctor: local + VPS + каждый агент)", ColorDim))
	fmt.Println("  [6] Синхронизация с VPS        " + Colorize("(подтянуть список роутеров с бэкенда)", ColorDim))
	fmt.Println("  [7] Открыть wizard.toml в редакторе")
	fmt.Println("  [8] Забыть known_hosts alias   " + Colorize("(если физически заменил роутер)", ColorDim))
	fmt.Println("  [9] Восстановить токен агента " + Colorize("(новый token_hash + переустановка config.yaml)", ColorDim))
	fmt.Println("  [10] Netfix маршрута          " + Colorize("(подсказать/применить /32 route через VPN)", ColorDim))
	fmt.Println("  [Q] Выход")
	fmt.Println()
}

func runActionAndSave(state *State, statePath string, secrets *SecretStore, fn func() error) {
	if err := fn(); err != nil {
		PrintFail("action failed: " + err.Error())
	}
	if err := SaveState(statePath, state); err != nil {
		PrintFail("save state: " + err.Error())
	}
	PrintSecretsSaveAdvice(secrets)
}

func openInEditor(path string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		if runtime.GOOS == "windows" {
			editor = "notepad"
		} else {
			editor = "nano"
		}
	}
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run() //nolint:errcheck
}

// askUninstallTarget builds the UninstallTarget the operator wants to
// clean. Two paths:
//  1. They pick a nickname from state.Agents — we use its known SSH coords.
//  2. They pick "другой" → manual host/port/user — typical "I accidentally
//     installed on a router not in wizard.toml" scenario.
func askUninstallTarget(state *State) UninstallTarget {
	if len(state.Agents) == 0 {
		PrintInfo("в wizard.toml нет агентов — введи параметры роутера руками")
		return UninstallTarget{}
	}
	fmt.Println("Выбери роутер для удаления агента:")
	for i, a := range state.Agents {
		fmt.Printf("  [%d] %s (%s:%d)\n", i+1, a.Nickname, a.Host, a.Port)
	}
	fmt.Printf("  [%d] другой (ввести host/port/user руками)\n", len(state.Agents)+1)
	idx := parseIntOr(Ask("номер", "1"), 1)
	if idx < 1 || idx > len(state.Agents)+1 {
		idx = 1
	}
	if idx == len(state.Agents)+1 {
		return UninstallTarget{}
	}
	a := state.Agents[idx-1]
	return UninstallTarget{Nickname: a.Nickname, Host: a.Host, Port: a.Port, User: a.User}
}
