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
		RunMenu(state, statePath, secrets, dl)
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
	case "install-agent":
		nick := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--agent" && i+1 < len(args) {
				nick = args[i+1]
			}
		}
		if err := actionInstallAgent(state, secrets, dl, nick); err != nil {
			os.Exit(1)
		}
		SaveState(statePath, state)
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
	case "add-router":
		if err := actionAddRouter(state, secrets, dl); err != nil {
			os.Exit(1)
		}
		SaveState(statePath, state)
		PrintSecretsSaveAdvice(secrets)
	case "status":
		if err := actionStatus(state, secrets); err != nil {
			os.Exit(1)
		}
	case "smoke":
		if err := actionSmokeTest(state, secrets); err != nil {
			os.Exit(1)
		}
	case "doctor":
		if err := actionDoctor(state, secrets); err != nil {
			os.Exit(1)
		}
	case "known-hosts":
		// known-hosts list | known-hosts forget [alias]
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		switch sub {
		case "list", "":
			path := defaultCacheDir() + "/known_hosts"
			aliases, err := ListKnownHostAliases(path)
			if err != nil {
				fmt.Fprintln(os.Stderr, "list:", err)
				os.Exit(1)
			}
			for _, a := range aliases {
				fmt.Println(a)
			}
		case "forget":
			if len(args) >= 3 {
				path := defaultCacheDir() + "/known_hosts"
				n, err := ForgetKnownHost(path, args[2])
				if err != nil {
					fmt.Fprintln(os.Stderr, "forget:", err)
					os.Exit(1)
				}
				fmt.Printf("removed %d entries for %s\n", n, args[2])
			} else if err := ForgetKnownHostInteractive(); err != nil {
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy known-hosts [list|forget [alias]]")
			os.Exit(2)
		}
	case "secrets":
		// secrets export <file.tgz> | secrets import <file.tgz> [--force]
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy secrets export <file.tgz> | secrets import <file.tgz> [--force]")
			os.Exit(2)
		}
		switch args[1] {
		case "export":
			if err := ExportSecrets(args[2], statePath); err != nil {
				fmt.Fprintln(os.Stderr, "export:", err)
				os.Exit(1)
			}
		case "import":
			force := false
			for i := 3; i < len(args); i++ {
				if args[i] == "--force" {
					force = true
				}
			}
			if err := ImportSecrets(args[2], statePath, force); err != nil {
				fmt.Fprintln(os.Stderr, "import:", err)
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "secrets: expected 'export' or 'import'")
			os.Exit(2)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		os.Exit(2)
	}
}
