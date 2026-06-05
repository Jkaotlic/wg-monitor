package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if filepath.Base(os.Args[0]) == "awgm-url-patch" {
		if err := actionAWGMURLPatch(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "awgm-url-patch:", err)
			os.Exit(1)
		}
		return
	}

	versionFlag := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "", "path to wizard.toml (default: platform default)")
	profile := flag.String("profile", "", "named deploy profile (isolates wizard.toml, cache, lock, and secrets)")
	noColor := flag.Bool("no-color", false, "disable ANSI colors")
	flag.Parse()
	setActiveProfile(*profile)

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
		case "awgm-url-patch":
			if err := actionAWGMURLPatch(args[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "awgm-url-patch:", err)
				os.Exit(1)
			}
			return
		}
	}

	statePath := *configPath
	if statePath == "" {
		if *profile != "" {
			statePath = ProfileStatePath(*profile)
		} else if _, err := os.Stat("wizard.toml"); err == nil {
			// cwd-fallback first, for repo-local dev
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
			fmt.Fprintln(os.Stderr, name+":", actErr)
			releaseLock()
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
	case "adopt-backend", "adopt-vps", "bind-backend":
		runCLIAction("adopt-backend", func() error { return actionAdoptBackend(state, secrets) })
	case "migrate-backend", "migrate-domain":
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
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy migrate-backend [--agent <nick>]")
				os.Exit(2)
			}
		}
		if agentFlag != "" && state.FindAgent(agentFlag) == nil {
			fmt.Fprintf(os.Stderr, "migrate-backend: agent %q не найден в wizard.toml\n", agentFlag)
			os.Exit(2)
		}
		runCLIAction("migrate-backend", func() error { return actionMigrateBackend(state, secrets, agentFlag) })
	case "restore-backup":
		opts := RestoreBackupOptions{}
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--dry-run", "--inspect":
				opts.DryRun = true
				opts.Mode = "dry-run"
			case "--to-current-vps":
				opts.Mode = "current"
			case "--to-new-vps":
				opts.Mode = "new"
			default:
				if strings.HasPrefix(args[i], "--") {
					fmt.Fprintf(os.Stderr, "restore-backup: unknown option %s\n", args[i])
					fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy restore-backup <archive.tgz> [--dry-run|--to-current-vps|--to-new-vps]")
					os.Exit(2)
				}
				if opts.ArchivePath == "" {
					opts.ArchivePath = args[i]
				} else {
					fmt.Fprintf(os.Stderr, "restore-backup: extra argument %s\n", args[i])
					os.Exit(2)
				}
			}
		}
		runCLIAction("restore-backup", func() error { return actionRestoreBackup(state, secrets, dl, opts) })
	case "deferred":
		if len(args) != 2 || args[1] != "status" {
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy deferred status")
			os.Exit(2)
		}
		runCLIAction("deferred status", func() error { return actionDeferredAWGMStatus(state, secrets) })
	case "backup":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy backup status|install|run|push-secrets|password|restore <archive>")
			os.Exit(2)
		}
		switch args[1] {
		case "status":
			if err := actionBackupStatus(state, secrets); err != nil {
				fmt.Fprintln(os.Stderr, "backup status:", err)
				os.Exit(1)
			}
		case "install":
			runCLIAction("backup install", func() error { return actionBackupInstall(state, secrets) })
		case "run":
			runCLIAction("backup run", func() error { return actionBackupRun(state, secrets) })
		case "push-secrets":
			runCLIAction("backup push-secrets", func() error { return actionBackupPushSecrets(state, secrets, statePath) })
		case "password":
			runCLIAction("backup password", func() error { return actionBackupPassword(state, secrets, true) })
		case "restore":
			if len(args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: wg-monitor-deploy backup restore <archive>")
				os.Exit(2)
			}
			runCLIAction("backup restore", func() error { return actionBackupRestore(state, secrets, args[2]) })
		default:
			fmt.Fprintln(os.Stderr, "backup: expected status, install, run, push-secrets, password or restore")
			os.Exit(2)
		}
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
	return `wg-monitor-deploy [--config wizard.toml] [--profile mobile] [--no-color] <command>

Commands:
  install-backend              first install or reinstall backend on VPS
  update-components, update-all
                               check latest release and update selected backend/agents
  update-backend               update only backend
  add-router                   enroll via VPS and install through AWG Manager/KeenDNS
  install-agent --agent <nick> install/reinstall through AWG Manager/KeenDNS
  update-agent [--agent <nick>]
                               update one agent or choose interactively
  uninstall-agent --agent <nick>
  uninstall-agent --host <ip> [--port N] [--user U]
                               remove agent from a known or manually specified router
  repair-agent-token [--agent <nick>]
                               re-enroll via AWG Manager and rotate agent token
  netfix [--agent <nick> | --host <ip> [--port N]] [--apply]
                               legacy recovery only when WG_LEGACY_ROUTER_SSH=1
  doctor [--deep]              local + VPS + agent health check
  sync-vps, sync               pull router list/deploy metadata from backend
  adopt-backend                bind this PC to an already running current VPS
  migrate-backend [--agent <nick>]
                               re-enroll agents on the new VPS through AWG Manager
  restore-backup <archive.tgz> [--dry-run|--to-current-vps|--to-new-vps]
                               inspect or restore a Telegram recovery bundle
  backup status|install|run|push-secrets|password|restore <archive>
                               manage encrypted nightly full backups
  deferred status              summarize deferred AWG Manager jobs on backend
  awgm-url-patch               emergency AWG Manager backend URL retarget helper
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
