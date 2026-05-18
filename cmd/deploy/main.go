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

	args := flag.Args()
	if len(args) > 0 {
		switch args[0] {
		case "help", "-h", "--help":
			printUsage()
			return
		}
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

	// Защита от двух wizard'ов одновременно — оба пишут в один wizard.toml
	// и secrets.env через temp+rename, но это атомарно per-write, не
	// межпроцессно: window where A loads, B loads, A saves, B saves =
	// потерянные правки A. Один operator-процесс на cache-dir.
	releaseLock, err := AcquirePIDLock()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	defer releaseLock()

	secrets := NewSecretStore()
	dl := NewDownloader()

	if len(args) == 0 {
		RunMenu(state, statePath, secrets, dl)
		return
	}

	// runCLIAction всегда сохраняет state, даже когда action упал. Это
	// важно: actions могут полу-применяться (state.Agents append, ag.Arch
	// set, etc.) до возврата ошибки, и потеря этих мутаций приводит к
	// orphan-данным в DB на VPS / disk-кэше / TG-топиках, которые wizard
	// больше никогда не увидит. Любая ошибка SaveState отдельно логируется,
	// но финальный exit-код приходит из original action error.
	runCLIAction := func(name string, fn func() error) {
		actErr := fn()
		if saveErr := SaveState(statePath, state); saveErr != nil {
			fmt.Fprintln(os.Stderr, name+": save state:", saveErr)
		}
		PrintSecretsSaveAdvice(secrets)
		if actErr != nil {
			os.Exit(1)
		}
	}

	switch args[0] {
	case "install-backend":
		runCLIAction("install-backend", func() error { return actionInstallBackend(state, secrets, dl) })
	case "update-backend":
		runCLIAction("update-backend", func() error { return actionUpdateBackend(state, secrets, dl) })
	case "update-components", "update-all":
		runCLIAction("update-components", func() error { return actionUpdateComponents(state, secrets, dl) })
	case "install-agent":
		nick := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--agent" && i+1 < len(args) {
				nick = args[i+1]
			}
		}
		// Если оператор передал --agent foo, foo должен уже быть в state
		// (создан через [3] add-router). Иначе actionInstallAgent сейчас
		// молча промптит nickname — что в неинтерактивном CLI повисает.
		if nick != "" && state.FindAgent(nick) == nil {
			fmt.Fprintf(os.Stderr,
				"install-agent: agent %q не найден в wizard.toml. Сначала запусти `wg-monitor-deploy add-router` (он создаст user'а в DB на VPS и сохранит токен).\n",
				nick)
			os.Exit(2)
		}
		runCLIAction("install-agent", func() error { return actionInstallAgent(state, secrets, dl, nick) })
	case "update-agent":
		agentFlag := ""
		for i := 1; i < len(args); i++ {
			if args[i] == "--agent" && i+1 < len(args) {
				agentFlag = args[i+1]
			}
		}
		if agentFlag != "" && state.FindAgent(agentFlag) == nil {
			fmt.Fprintf(os.Stderr, "update-agent: agent %q не найден в wizard.toml\n", agentFlag)
			os.Exit(2)
		}
		runCLIAction("update-agent", func() error { return actionUpdateAgent(state, secrets, dl, agentFlag) })
	case "add-router":
		runCLIAction("add-router", func() error { return actionAddRouter(state, secrets, dl) })
	case "sync-vps", "sync":
		runCLIAction("sync-vps", func() error { return actionSyncVPS(state, secrets) })
	case "uninstall-agent":
		// uninstall-agent --agent <nick>          (looks up SSH coords in state.Agents)
		// uninstall-agent --host <ip> [--port N] [--user U]  (manual, for routers
		// not in wizard.toml — typical "accidentally installed on local box")
		target := UninstallTarget{Port: 222, User: "root"}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--agent":
				if i+1 < len(args) {
					target.Nickname = args[i+1]
					i++
				}
			case "--host":
				if i+1 < len(args) {
					target.Host = args[i+1]
					i++
				}
			case "--port":
				if i+1 < len(args) {
					var p int
					fmt.Sscanf(args[i+1], "%d", &p)
					if p > 0 {
						target.Port = p
					}
					i++
				}
			case "--user":
				if i+1 < len(args) {
					target.User = args[i+1]
					i++
				}
			}
		}
		if target.Nickname == "" && target.Host == "" {
			fmt.Fprintln(os.Stderr, "uninstall-agent: укажи --agent <nickname> или --host <ip> [--port N] [--user U]")
			os.Exit(2)
		}
		if target.Nickname != "" && state.FindAgent(target.Nickname) == nil {
			fmt.Fprintf(os.Stderr, "uninstall-agent: agent %q не найден в wizard.toml\n", target.Nickname)
			os.Exit(2)
		}
		runCLIAction("uninstall-agent", func() error { return actionUninstallAgent(state, secrets, target) })
	case "repair-agent-token", "restore-agent-token":
		agentFlag := ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--agent":
				if i+1 < len(args) {
					agentFlag = args[i+1]
					i++
				}
			default:
				fmt.Fprintf(os.Stderr, "%s: unknown option %s\n", args[0], args[i])
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy repair-agent-token [--agent <nick>]")
				os.Exit(2)
			}
		}
		runCLIAction("repair-agent-token", func() error { return actionRepairAgentToken(state, secrets, dl, agentFlag) })
	case "netfix", "route-fix":
		opts, err := parseNetfixArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "netfix:", err)
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy netfix [--agent <nick> | --host <ip> [--port N]] [--apply]")
			os.Exit(2)
		}
		if err := actionNetfix(state, opts); err != nil {
			os.Exit(1)
		}
	case "doctor":
		opts := doctorOptions{}
		for _, arg := range args[1:] {
			switch arg {
			case "--deep":
				opts.Deep = true
			default:
				fmt.Fprintf(os.Stderr, "doctor: unknown option %s\n", arg)
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy doctor [--deep]")
				os.Exit(2)
			}
		}
		if err := actionDoctor(state, secrets, opts); err != nil {
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
			} else if err := ForgetKnownHostInteractive(state); err != nil {
				os.Exit(1)
			}
		default:
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy known-hosts [list|forget [alias]]")
			os.Exit(2)
		}
	case "secrets":
		// secrets status | secrets export <file.tgz> | secrets import <file.tgz> [--force]
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy secrets status | secrets export <file.tgz> | secrets import <file.tgz> [--force]")
			os.Exit(2)
		}
		switch args[1] {
		case "status":
			if err := actionSecretsStatus(state, secrets); err != nil {
				fmt.Fprintln(os.Stderr, "status:", err)
				os.Exit(1)
			}
		case "export":
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy secrets export <file.tgz>")
				os.Exit(2)
			}
			if err := ExportSecrets(args[2], statePath); err != nil {
				fmt.Fprintln(os.Stderr, "export:", err)
				os.Exit(1)
			}
		case "import":
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy secrets import <file.tgz> [--force]")
				os.Exit(2)
			}
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
			fmt.Fprintln(os.Stderr, "secrets: expected 'status', 'export' or 'import'")
			os.Exit(2)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n\n", args[0])
		printUsage()
		os.Exit(2)
	}
}

func usageText() string {
	return `wg-monitor-deploy [--config wizard.toml] [--no-color] <command>

Commands:
  install-backend              first install or reinstall backend on VPS
  update-components, update-all
                               check latest release and update selected backend/agents
  update-backend               update only backend
  add-router                   create router in backend DB, create TG topic, install agent
  install-agent --agent <nick> install/reinstall an existing router agent
  update-agent [--agent <nick>]
                               update one agent or choose interactively
  uninstall-agent --agent <nick>
  uninstall-agent --host <ip> [--port N] [--user U]
                               remove agent from a known or manually specified router
  repair-agent-token [--agent <nick>]
                               rotate lost agent token, reinstall router config
  netfix [--agent <nick> | --host <ip> [--port N]] [--apply]
                               show/apply a /32 route through VPN/WireGuard
  doctor [--deep]              local + VPS + agent health check
  sync-vps, sync               pull router list/deploy metadata from backend
  known-hosts [list|forget [alias]]
  secrets status               show which required secrets are available
  secrets export <file.tgz>
  secrets import <file.tgz> [--force]
  help                         show this help

Without a command, the interactive wizard menu opens.
`
}

func printUsage() {
	fmt.Fprint(os.Stdout, usageText())
}
