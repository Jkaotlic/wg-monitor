package actions

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

var validNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

const defaultImportBackend = "nativewg"

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
	case "S3":
		return parseInt(&iface.S3)
	case "S4":
		return parseInt(&iface.S4)
	// H1-H4 are passed as-is (single value "1234567890" or range "lo-hi").
	case "H1":
		iface.H1 = val
	case "H2":
		iface.H2 = val
	case "H3":
		iface.H3 = val
	case "H4":
		iface.H4 = val
	}
	return nil
}

func parseWGConfAddress(data string) string {
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
		if section != "interface" {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(kv[0]), "Address") {
			return normalizeWGAddress(strings.TrimSpace(kv[1]))
		}
	}
	return ""
}

func normalizeWGAddress(s string) string {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
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

// preferredBackend returns the backend for newly imported provider configs.
// Amnezia/HideMy configs must be created through NativeWG; inheriting "kernel"
// from an older tunnel makes fresh imports come up with the wrong engine.
func preferredBackend(ctx context.Context, client *awgmgr.Client, requested string) string {
	if requested = strings.TrimSpace(strings.ToLower(requested)); requested != "" {
		return requested
	}
	all, err := client.TunnelsAll(ctx)
	if err != nil || len(all.Tunnels) == 0 {
		return defaultImportBackend
	}
	for _, t := range all.Tunnels {
		if strings.EqualFold(strings.TrimSpace(t.Backend), defaultImportBackend) {
			return defaultImportBackend
		}
	}
	return defaultImportBackend
}

// ImportTunnel is the agent-side handler for the tunnel_import wire.Command.
// confB64 is base64-encoded .conf content.
// replace=true  → find existing tunnel by name and use ReplaceConf API (atomic).
// replace=false → ImportConf (creates new tunnel, enabled=false).
// Restarts HydraRoute daemon if installed.
func ImportTunnel(ctx context.Context, client *awgmgr.Client, exec ExecFunc, confB64, name string, replace bool, requestedBackend string) (string, error) {
	slog.Info("tunnel import", "name", name, "replace", replace)
	confData, err := base64.StdEncoding.DecodeString(confB64)
	if err != nil {
		slog.Warn("tunnel import failed", "name", name, "stage", "decode", "err", err)
		return "", fmt.Errorf("decode conf: %w", err)
	}
	rawConf := string(confData)

	// Validate required fields before hitting awg-manager.
	if _, err := ParseWGConf(rawConf); err != nil {
		slog.Warn("tunnel import failed", "name", name, "stage", "parse", "err", err)
		return "", fmt.Errorf("parse conf: %w", err)
	}

	// Determine preferred backend from existing tunnels so the new tunnel
	// uses the same backend (e.g. nativewg) rather than the system default.
	backend := preferredBackend(ctx, client, requestedBackend)

	var result strings.Builder
	var newID string
	if replace {
		all, err := client.TunnelsAll(ctx)
		if err != nil {
			slog.Warn("tunnel import failed", "name", name, "stage", "list", "err", err)
			return "", fmt.Errorf("list tunnels: %w", err)
		}
		var oldID string
		for _, t := range all.Tunnels {
			if t.Name == name {
				oldID = t.ID
				break
			}
		}
		if oldID == "" {
			confAddress := parseWGConfAddress(rawConf)
			for _, t := range all.Tunnels {
				if confAddress != "" && normalizeWGAddress(t.Address) == confAddress {
					oldID = t.ID
					break
				}
			}
		}
		if oldID != "" {
			newTun, err := client.ReplaceConf(ctx, oldID, rawConf, name, backend)
			if err != nil {
				slog.Warn("tunnel import failed", "name", name, "stage", "replace", "old_id", oldID, "err", err)
				return "", fmt.Errorf("replace tunnel: %w", err)
			}
			newID = newTun.ID
			fmt.Fprintf(&result, "✅ Туннель %q заменён (id=%s)", name, newTun.ID)
		} else {
			newTun, err := client.ImportConf(ctx, rawConf, name, backend)
			if err != nil {
				slog.Warn("tunnel import failed", "name", name, "stage", "create", "err", err)
				return "", fmt.Errorf("create tunnel: %w", err)
			}
			newID = newTun.ID
			fmt.Fprintf(&result, "✅ Туннель %q создан (id=%s)", name, newTun.ID)
		}
	} else {
		newTun, err := client.ImportConf(ctx, rawConf, name, backend)
		if err != nil {
			slog.Warn("tunnel import failed", "name", name, "stage", "create", "err", err)
			return "", fmt.Errorf("create tunnel: %w", err)
		}
		newID = newTun.ID
		fmt.Fprintf(&result, "✅ Туннель %q создан (id=%s)", name, newTun.ID)
	}

	if err := client.StartTunnel(ctx, newID); err != nil {
		slog.Warn("tunnel import: start failed", "name", name, "id", newID, "err", err)
		fmt.Fprintf(&result, "\n⚠️ Запустить туннель не удалось: %v", err)
	}

	if hs, err := client.HydraRouteStatus(ctx); err == nil && hs.Installed {
		out, execErr := exec(ctx, "/opt/etc/init.d/S99hrneo", "restart")
		if execErr != nil {
			slog.Warn("tunnel import: hydraroute restart failed", "name", name, "err", execErr)
			fmt.Fprintf(&result, "\n⚠️ HydraRoute restart failed: %v\n%s", execErr, string(out))
		} else {
			fmt.Fprintf(&result, "\n🔁 HydraRoute перезапущен")
		}
	}

	slog.Info("tunnel import ok", "name", name, "id", newID, "replace", replace)
	return result.String(), nil
}
