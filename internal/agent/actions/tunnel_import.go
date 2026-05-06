package actions

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

var validNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

func isValidTunnelName(s string) bool { return validNameRe.MatchString(s) }

func sanitizeTunnelName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ParseWGConf parses a WireGuard / AmneziaWG .conf into a CreateTunnelRequest.
// Name and DefaultRoute must be set by the caller after parsing.
func ParseWGConf(data string) (awgmgr.CreateTunnelRequest, error) {
	var req awgmgr.CreateTunnelRequest
	req.Type = "amnezia_wg"
	req.Enabled = true

	section := ""
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line[1 : len(line)-1])
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])
		switch section {
		case "interface":
			if err := parseInterfaceField(&req.Interface, key, val); err != nil {
				return req, fmt.Errorf("parse [Interface] %s: %w", key, err)
			}
		case "peer":
			if err := parsePeerField(&req.Peer, key, val); err != nil {
				return req, fmt.Errorf("parse [Peer] %s: %w", key, err)
			}
		}
	}
	if req.Interface.PrivateKey == "" {
		return req, fmt.Errorf("missing PrivateKey in [Interface]")
	}
	if req.Peer.PublicKey == "" {
		return req, fmt.Errorf("missing PublicKey in [Peer]")
	}
	if req.Peer.Endpoint == "" {
		return req, fmt.Errorf("missing Endpoint in [Peer]")
	}
	return req, nil
}

func parseInterfaceField(iface *awgmgr.InterfaceConfig, key, val string) error {
	parseInt := func(dst *int) error {
		n, err := strconv.Atoi(val)
		if err != nil {
			return err
		}
		*dst = n
		return nil
	}
	parseU32 := func(dst *uint32) error {
		n, err := strconv.ParseUint(val, 10, 32)
		if err != nil {
			return err
		}
		*dst = uint32(n)
		return nil
	}
	switch key {
	case "PrivateKey":
		iface.PrivateKey = val
	case "Jc":
		return parseInt(&iface.Jc)
	case "Jmin":
		return parseInt(&iface.Jmin)
	case "Jmax":
		return parseInt(&iface.Jmax)
	case "S1":
		return parseInt(&iface.S1)
	case "S2":
		return parseInt(&iface.S2)
	case "H1":
		return parseU32(&iface.H1)
	case "H2":
		return parseU32(&iface.H2)
	case "H3":
		return parseU32(&iface.H3)
	case "H4":
		return parseU32(&iface.H4)
	}
	return nil
}

func parsePeerField(peer *awgmgr.PeerConfig, key, val string) error {
	switch key {
	case "PublicKey":
		peer.PublicKey = val
	case "PresharedKey":
		peer.PresharedKey = val
	case "Endpoint":
		peer.Endpoint = val
	case "AllowedIPs":
		for _, p := range strings.Split(val, ",") {
			if p = strings.TrimSpace(p); p != "" {
				peer.AllowedIPs = append(peer.AllowedIPs, p)
			}
		}
	}
	return nil
}

// ImportTunnel is the agent-side handler for the tunnel_import wire.Command.
// confB64 is base64-encoded .conf content. If replace=true, finds the tunnel
// by name in awg-manager and deletes it AFTER successful create.
// Restarts HydraRoute daemon if installed.
func ImportTunnel(ctx context.Context, client *awgmgr.Client, exec ExecFunc, confB64, name string, replace bool) (string, error) {
	confData, err := base64.StdEncoding.DecodeString(confB64)
	if err != nil {
		return "", fmt.Errorf("decode conf: %w", err)
	}

	req, err := ParseWGConf(string(confData))
	if err != nil {
		return "", fmt.Errorf("parse conf: %w", err)
	}
	req.Name = name

	var oldTunnelID string
	if replace {
		all, err := client.TunnelsAll(ctx)
		if err != nil {
			return "", fmt.Errorf("list tunnels: %w", err)
		}
		for _, t := range all.Tunnels {
			if t.Name == name {
				oldTunnelID = t.ID
				req.DefaultRoute = t.DefaultRoute
				break
			}
		}
		if oldTunnelID == "" {
			req.DefaultRoute = true
		}
	} else {
		req.DefaultRoute = false
	}

	newTun, err := client.CreateTunnel(ctx, req)
	if err != nil {
		return "", fmt.Errorf("create tunnel: %w", err)
	}

	var result strings.Builder
	fmt.Fprintf(&result, "✅ Туннель %q создан (id=%s)", name, newTun.ID)

	if oldTunnelID != "" {
		if err := client.DeleteTunnel(ctx, oldTunnelID); err != nil {
			fmt.Fprintf(&result, "\n⚠️ Удалить старый туннель не удалось: %v", err)
		} else {
			fmt.Fprintf(&result, "\n🗑 Старый туннель удалён")
		}
	}

	if hs, err := client.HydraRouteStatus(ctx); err == nil && hs.Installed {
		out, execErr := exec(ctx, "/opt/etc/init.d/S99hrneo", "restart")
		if execErr != nil {
			fmt.Fprintf(&result, "\n⚠️ HydraRoute restart failed: %v\n%s", execErr, string(out))
		} else {
			fmt.Fprintf(&result, "\n🔁 HydraRoute перезапущен")
		}
	}

	return result.String(), nil
}
