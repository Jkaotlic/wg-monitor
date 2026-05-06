package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func RunMenu(state *State, statePath string, secrets *SecretStore, dl *Downloader) {
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
				return actionUpdateBackend(state, secrets, dl)
			})
		case "3":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionInstallAgent(state, secrets, dl, "")
			})
		case "4":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionUpdateAgent(state, secrets, dl, "")
			})
		case "5":
			runActionAndSave(state, statePath, secrets, func() error {
				return actionAddRouter(state, secrets, dl)
			})
		case "6":
			actionStatus(state, secrets) //nolint:errcheck
		case "7":
			openInEditor(statePath)
			// Reload after edit.
			if reloaded, err := LoadState(statePath); err == nil {
				*state = *reloaded
			}
		case "Q", "":
			return
		default:
			PrintFail("Не понял. Введи 1–7 или Q.")
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
	fmt.Println("  [1] Первичная установка бэкенда на VPS")
	if state.Backend.Host != "" {
		fmt.Printf("  [2] Обновить бэкенд на VPS  %s\n",
			Colorize("(last: "+state.Backend.LastDeploy+")", ColorDim))
	} else {
		fmt.Println("  [2] Обновить бэкенд на VPS  " + Colorize("(сначала установи)", ColorDim))
	}
	fmt.Println("  [3] Первичная установка агента на роутер")
	if len(state.Agents) > 0 {
		fmt.Printf("  [4] Обновить агента на роутере  %s\n",
			Colorize("(last: "+state.Agents[0].LastDeploy+")", ColorDim))
	} else {
		fmt.Println("  [4] Обновить агента на роутере  " + Colorize("(сначала установи)", ColorDim))
	}
	fmt.Println("  [5] Добавить новый роутер")
	fmt.Println("  [6] Проверить статус")
	fmt.Println("  [7] Открыть wizard.toml в редакторе")
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
