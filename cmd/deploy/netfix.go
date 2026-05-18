package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

type netfixOptions struct {
	Agent string
	Host  string
	Port  int
	Apply bool
}

func actionNetfix(state *State, opts netfixOptions) error {
	targetHost, targetPort, label, err := netfixTarget(state, opts)
	if err != nil {
		return err
	}
	ip := net.ParseIP(targetHost)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("netfix сейчас работает с IPv4 literal, got %q", targetHost)
	}
	if targetPort == 0 {
		targetPort = 222
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("enumerate interfaces: %w", err)
	}
	tunnels := tunnelInterfaces(ifaces)
	if len(tunnels) == 0 {
		PrintFail("не нашёл UP VPN/WireGuard интерфейсов")
		fmt.Println("  Проверь, что WireGuard-клиент подключён, потом повтори netfix.")
		return fmt.Errorf("no tunnel interfaces")
	}

	fmt.Println(Colorize("=== netfix ===", ColorBold))
	fmt.Printf("Target: %s:%d", targetHost, targetPort)
	if label != "" {
		fmt.Printf(" (%s)", label)
	}
	fmt.Println()
	fmt.Println("VPN/P2P interfaces:")
	for i, iface := range tunnels {
		local := firstIPv4(iface)
		if local == "" {
			local = "no IPv4"
		}
		fmt.Printf("  [%d] %s  ifIndex=%d  %s\n", i+1, iface.Name, iface.Index, local)
	}

	chosen := tunnels[0]
	if len(tunnels) > 1 && os.Getenv("WG_YES_TO_ALL") != "1" {
		idx := parseIntOr(Ask("номер VPN-интерфейса", "1"), 1)
		if idx < 1 || idx > len(tunnels) {
			idx = 1
		}
		chosen = tunnels[idx-1]
	}

	cand := PathCandidate{
		Iface:   chosen.Name,
		Index:   chosen.Index,
		LocalIP: firstIPv4(chosen),
		Kind:    PathP2P,
	}
	addCmd, delCmd := manualRouteCommands(targetHost, chosen)
	fmt.Println()
	PrintInfo("Команды для ручного применения:")
	fmt.Println("  " + addCmd)
	fmt.Println("  " + delCmd)
	fmt.Println("  WireGuard AllowedIPs long-term: " + targetHost + "/32")

	if !opts.Apply {
		fmt.Println()
		if !Confirm("Применить этот route сейчас?", false) {
			PrintInfo("Ок, ничего не меняю. Для авто-применения без вопроса: netfix --agent <nick> --apply или netfix --host " + targetHost + " --apply")
			return nil
		}
	}

	PrintStep(1, 2, "Добавить /32 route через "+chosen.Name)
	tok, err := addTempHostRoute(targetHost, chosen.Index)
	if err != nil {
		fmt.Print(manualRouteHint(targetHost, cand))
		return err
	}
	_ = tok
	PrintOK("route добавлен: " + addCmd)
	PrintWarn("route оставлен активным для следующих wizard-команд. Снять вручную: " + delCmd)

	PrintStep(2, 2, fmt.Sprintf("Проверить TCP %s:%d", targetHost, targetPort))
	if dialableTCP(targetHost, targetPort, 3*time.Second) {
		PrintOK(fmt.Sprintf("TCP %s:%d reachable", targetHost, targetPort))
		return nil
	}
	PrintWarn(fmt.Sprintf("TCP %s:%d всё ещё не отвечает. Возможно, порт другой, firewall, или WireGuard peer не маршрутизирует этот адрес.", targetHost, targetPort))
	return nil
}

func netfixTarget(state *State, opts netfixOptions) (host string, port int, label string, err error) {
	if opts.Host != "" {
		return opts.Host, opts.Port, "manual", nil
	}
	ag, err := chooseAgentForAction(state, opts.Agent, "настройки сетевого маршрута")
	if err != nil {
		return "", 0, "", err
	}
	return ag.Host, portOrDefault(ag.Port, 222), ag.Nickname, nil
}

func tunnelInterfaces(ifaces []net.Interface) []net.Interface {
	out := make([]net.Interface, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if classifyIface(iface) == PathP2P {
			out = append(out, iface)
		}
	}
	return out
}

func parseNetfixArgs(args []string) (netfixOptions, error) {
	var opts netfixOptions
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--agent":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--agent requires value")
			}
			opts.Agent = args[i+1]
			i++
		case "--host":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--host requires value")
			}
			opts.Host = args[i+1]
			i++
		case "--port":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--port requires value")
			}
			fmt.Sscanf(args[i+1], "%d", &opts.Port)
			i++
		case "--apply":
			opts.Apply = true
		default:
			return opts, fmt.Errorf("unknown option %s", args[i])
		}
	}
	if opts.Agent != "" && opts.Host != "" {
		return opts, fmt.Errorf("use either --agent or --host, not both")
	}
	if opts.Host != "" {
		opts.Host = strings.TrimSpace(opts.Host)
	}
	return opts, nil
}
