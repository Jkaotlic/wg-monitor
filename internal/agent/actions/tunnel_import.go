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
	"time"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
)

var validNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

const defaultImportBackend = "nativeWG"

var importVerifyDelays = []time.Duration{
	3 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
}

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
	if requested = normalizeImportBackend(requested); requested != "" {
		return requested
	}
	all, err := client.TunnelsAll(ctx)
	if err != nil || len(all.Tunnels) == 0 {
		return defaultImportBackend
	}
	for _, t := range all.Tunnels {
		if normalizeImportBackend(t.Backend) == defaultImportBackend {
			return defaultImportBackend
		}
	}
	return defaultImportBackend
}

func normalizeImportBackend(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return ""
	case "nativewg", "native_wg", "native-wg":
		return defaultImportBackend
	default:
		return strings.TrimSpace(s)
	}
}

func shouldRecreateTunnelForBackend(t awgmgr.Tunnel, requestedBackend string) bool {
	if normalizeImportBackend(requestedBackend) != defaultImportBackend {
		return false
	}
	oldBackend := normalizeImportBackend(tunnelBackend(t))
	return oldBackend != "" && oldBackend != defaultImportBackend
}

func tunnelBackend(t awgmgr.Tunnel) string {
	if strings.TrimSpace(t.Backend) != "" {
		return t.Backend
	}
	return t.BackendType
}

// ImportTunnel is the agent-side handler for the tunnel_import wire.Command.
// confB64 is base64-encoded .conf content.
// replace=true  → find existing tunnel by name and use ReplaceConf API (atomic).
// replace=false → ImportConf (creates new tunnel, enabled=false).
// Restarts HydraRoute daemon if installed.
func ImportTunnel(ctx context.Context, client *awgmgr.Client, exec ExecFunc, sleep func(context.Context, time.Duration) error, confB64, name string, replace bool, requestedBackend string) (string, error) {
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
		var oldTunnel *awgmgr.Tunnel
		for _, t := range all.Tunnels {
			if t.Name == name {
				t := t
				oldTunnel = &t
				break
			}
		}
		if oldTunnel == nil {
			confAddress := parseWGConfAddress(rawConf)
			for _, t := range all.Tunnels {
				if confAddress != "" && normalizeWGAddress(t.Address) == confAddress {
					t := t
					oldTunnel = &t
					break
				}
			}
		}
		if oldTunnel != nil && shouldRecreateTunnelForBackend(*oldTunnel, backend) {
			if err := client.DeleteTunnel(ctx, oldTunnel.ID); err != nil {
				slog.Warn("tunnel import failed", "name", name, "stage", "delete-old-backend", "old_id", oldTunnel.ID, "old_backend", tunnelBackend(*oldTunnel), "err", err)
				return "", fmt.Errorf("delete old tunnel before nativeWG recreate: %w", err)
			}
			newTun, err := client.ImportConf(ctx, rawConf, name, backend)
			if err != nil {
				slog.Warn("tunnel import failed", "name", name, "stage", "recreate-nativewg", "old_id", oldTunnel.ID, "old_backend", tunnelBackend(*oldTunnel), "err", err)
				return "", fmt.Errorf("recreate tunnel as nativeWG: %w", err)
			}
			newID = newTun.ID
			fmt.Fprintf(&result, "✅ Туннель %q пересоздан в nativeWG (id=%s)", name, newTun.ID)
		} else if oldTunnel != nil {
			newTun, err := client.ReplaceConf(ctx, oldTunnel.ID, rawConf, name, backend)
			if err != nil {
				slog.Warn("tunnel import failed", "name", name, "stage", "replace", "old_id", oldTunnel.ID, "err", err)
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

	if line := verifyImportedTunnel(ctx, client, sleep, newID); line != "" {
		fmt.Fprintf(&result, "\n%s", line)
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

func verifyImportedTunnel(ctx context.Context, client *awgmgr.Client, sleep func(context.Context, time.Duration) error, tunnelID string) string {
	if tunnelID == "" {
		return "⚠️ Проверка запуска: awg-manager не вернул id туннеля"
	}
	var last *awgmgr.Tunnel
	var lastErr error
	for i, delay := range importVerifyDelays {
		if err := sleepForImportVerify(ctx, sleep, delay); err != nil {
			return fmt.Sprintf("⚠️ Проверка запуска прервана: %v", err)
		}
		tu, err := findTunnelByID(ctx, client, tunnelID)
		if err != nil {
			lastErr = err
			continue
		}
		last = tu
		if tunnelLooksStarted(*tu) {
			return fmt.Sprintf("✅ Проверка запуска: status=running, handshake=%s, iface=%s", handshakeLabel(*tu), tu.InterfaceName)
		}
		if i == 1 && tu.Enabled && tu.Status != "running" {
			_ = client.StartTunnel(ctx, tunnelID)
		}
	}
	if last != nil {
		return fmt.Sprintf("⚠️ Проверка запуска: туннель импортирован, но пока не ожил (%s)", tunnelStartSummary(*last))
	}
	if lastErr != nil {
		return fmt.Sprintf("⚠️ Проверка запуска: не удалось перечитать туннель после импорта: %v", lastErr)
	}
	return "⚠️ Проверка запуска: туннель не найден после импорта"
}

func sleepForImportVerify(ctx context.Context, sleep func(context.Context, time.Duration) error, d time.Duration) error {
	if sleep != nil {
		return sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func findTunnelByID(ctx context.Context, client *awgmgr.Client, tunnelID string) (*awgmgr.Tunnel, error) {
	all, err := client.TunnelsAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, tu := range all.Tunnels {
		if tu.ID == tunnelID {
			tu := tu
			return &tu, nil
		}
	}
	return nil, fmt.Errorf("tunnel %s not found", tunnelID)
}

func tunnelLooksStarted(tu awgmgr.Tunnel) bool {
	return tu.Enabled && tu.Status == "running" && tu.LastHandshake.Time() != nil
}

func handshakeLabel(tu awgmgr.Tunnel) string {
	if t := tu.LastHandshake.Time(); t != nil {
		return t.UTC().Format(time.RFC3339)
	}
	return "none"
}

func tunnelStartSummary(tu awgmgr.Tunnel) string {
	parts := []string{
		"id=" + tu.ID,
		"status=" + strings.TrimSpace(tu.Status),
		fmt.Sprintf("enabled=%t", tu.Enabled),
		"handshake=" + handshakeLabel(tu),
	}
	if tu.InterfaceName != "" {
		parts = append(parts, "iface="+tu.InterfaceName)
	}
	if tu.Backend != "" {
		parts = append(parts, "backend="+tu.Backend)
	}
	if tu.HasAddressConflict {
		parts = append(parts, "address_conflict=true")
	}
	if tu.RxBytes > 0 || tu.TxBytes > 0 {
		parts = append(parts, fmt.Sprintf("rx=%d tx=%d", tu.RxBytes, tu.TxBytes))
	}
	return strings.Join(parts, ", ")
}
