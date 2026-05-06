package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	versionFlag := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "", "path to wizard.toml (default: platform default)")
	noColor := flag.Bool("no-color", false, "disable ANSI colors")
	flag.Parse()

	if *versionFlag {
		fmt.Println(Version)
		return
	}
	if *noColor {
		UseColor = false
	}

	statePath := *configPath
	if statePath == "" {
		// cwd-fallback first, for repo-local dev
		if _, err := os.Stat("wizard.toml"); err == nil {
			statePath = "wizard.toml"
		} else {
			statePath = DefaultStatePath()
		}
	}
	state, err := LoadState(statePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load state:", err)
		os.Exit(1)
	}
	secrets := NewSecretStore()
	dl := NewDownloader()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("wg-monitor-deploy", Version, "— меню в Task 12")
		fmt.Println("пока доступно: update-backend")
		return
	}

	switch args[0] {
	case "install-backend":
		if err := actionInstallBackend(state, secrets, dl); err != nil {
			os.Exit(1)
		}
		SaveState(statePath, state)
		PrintSecretsSaveAdvice(secrets)
	case "update-backend":
		if err := actionUpdateBackend(state, secrets, dl); err != nil {
			os.Exit(1)
		}
		if err := SaveState(statePath, state); err != nil {
			fmt.Fprintln(os.Stderr, "save state:", err)
		}
		PrintSecretsSaveAdvice(secrets)
	case "update-agent":
		agentFlag := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--agent" && i+1 < len(args) {
				agentFlag = args[i+1]
			}
		}
		if err := actionUpdateAgent(state, secrets, dl, agentFlag); err != nil {
			os.Exit(1)
		}
		if err := SaveState(statePath, state); err != nil {
			fmt.Fprintln(os.Stderr, "save state:", err)
		}
		PrintSecretsSaveAdvice(secrets)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}
