package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/pkg/wire"
)

// GetFirmwareStatus runs `ndmc -c "components list"` and parses the output
// into a wire.FirmwareStatus. The command returns two YAML-ish blocks:
// `firmware:` (server-side release for the current sandbox) and `local:`
// (what is installed). If they differ, an update is available.
func GetFirmwareStatus(ctx context.Context, exec ExecFunc) (wire.FirmwareStatus, error) {
	out, err := exec(ctx, "ndmc", "-c", "components list")
	if err != nil {
		return wire.FirmwareStatus{}, fmt.Errorf("ndmc components list: %w", err)
	}
	return parseComponentsList(string(out))
}

// InstallFirmware kicks the KeeneticOS firmware install via
// `ndmc -c "components commit"`. After the call returns successfully the
// router will reboot within seconds — the agent will lose connection long
// before any follow-up work can complete; the caller should not expect
// further status updates from this command.
func InstallFirmware(ctx context.Context, exec ExecFunc) error {
	if _, err := exec(ctx, "ndmc", "-c", "components commit"); err != nil {
		return fmt.Errorf("ndmc components commit: %w", err)
	}
	return nil
}

// parseComponentsList is the format parser, separated for table-driven tests.
//
// Format observed on testkeen (M0 Probe 4): ndmc emits an `\x1b[K` ANSI
// erase-line escape at the start of output, then YAML-ish indented blocks.
// Leading whitespace per line is variable (10-16 spaces); we use suffix
// matching of trimmed prefixes rather than fixed-column parsing.
//
// We track which top-level block we're inside (`firmware:` vs `local:`) by
// detecting block headers (lines whose trimmed text equals "firmware:" or
// "local:"), then read the `version:` and `sandbox:` keys within them.
func parseComponentsList(s string) (wire.FirmwareStatus, error) {
	var fs wire.FirmwareStatus
	var firmwareVersion, localVersion string
	block := "" // "firmware" | "local" | "" (top-level)
	for _, raw := range strings.Split(s, "\n") {
		// Strip ANSI erase-line escape if present at the very start of a line.
		line := strings.TrimPrefix(raw, "\x1b[K")
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "firmware:":
			block = "firmware"
			continue
		case "local:":
			block = "local"
			continue
		}
		// Within current block, harvest version: / sandbox: keys.
		if k, v, ok := splitKV(trimmed); ok {
			switch {
			case k == "version" && block == "firmware":
				firmwareVersion = v
			case k == "version" && block == "local":
				localVersion = v
			case k == "sandbox" && block == "local":
				// Ignore local.sandbox — we only want the top-level channel.
			case k == "sandbox":
				// Top-level sandbox: line (appears between firmware: and local:
				// blocks); capture it and reset block context so subsequent
				// unrelated keys don't pollute the firmware block.
				fs.Channel = v
				block = ""
			}
		}
	}
	if localVersion == "" {
		return fs, fmt.Errorf("could not extract local.version from `ndmc components list` output")
	}
	fs.Current = localVersion
	if firmwareVersion != "" && firmwareVersion != localVersion {
		fs.Available = firmwareVersion
	}
	return fs, nil
}

// splitKV splits a "key: value" line, returning ok=false if not formatted that way.
func splitKV(line string) (key, value string, ok bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

// AwgInfoClient is the subset of *awgmgr.Client that VersionAudit consumes.
// Defined as an interface so tests can stub the awg-manager API.
type AwgInfoClient interface {
	SystemInfo(ctx context.Context) (*awgmgr.SystemInfo, error)
	HydraRouteStatus(ctx context.Context) (*awgmgr.HydraRouteStatus, error)
}

// VersionAudit collects installed versions and daemon uptimes for the
// Maintenance panel. Sources:
//   - awgmgr version + firmware: awgmgr SystemInfo.
//   - hrneo version: `opkg info hrneo` (no API; HRStatus has no version field).
//   - hrneo uptime, awgmgr uptime: /proc/$pid/stat starttime (jiffies, USER_HZ=100)
//     subtracted from /proc/uptime system uptime.
//   - firmware available: `ndmc components list` — populated only when the
//     server-side release differs from the locally installed version.
//
// Best-effort everywhere: if hrneo isn't installed, hrneo fields stay empty;
// if a daemon isn't running, its uptime stays empty; only the awgmgr SystemInfo
// fetch is treated as fatal (without it we cannot render anything useful).
func VersionAudit(ctx context.Context, awg AwgInfoClient, exec ExecFunc) (wire.VersionAudit, error) {
	sys, err := awg.SystemInfo(ctx)
	if err != nil {
		return wire.VersionAudit{}, fmt.Errorf("awgmgr SystemInfo: %w", err)
	}
	out := wire.VersionAudit{
		AwgmgrVersion:   sys.Version,
		FirmwareCurrent: sys.FirmwareVersion,
	}
	// hrneo: only populate when actually installed.
	if hr, herr := awg.HydraRouteStatus(ctx); herr == nil && hr != nil && hr.Installed {
		if v, err := opkgHrneoVersion(ctx, exec); err == nil {
			out.HrneoVersion = v
		}
	}
	// firmware available: from components list. Tolerate failure (panel still useful).
	if fs, ferr := GetFirmwareStatus(ctx, exec); ferr == nil {
		// Trust components-list firmware field over SystemInfo for current too —
		// they should match, but components-list is what we use to detect updates.
		if fs.Current != "" {
			out.FirmwareCurrent = fs.Current
		}
		out.FirmwareAvail = fs.Available
	}
	// Read /proc/uptime once, then per-daemon stat.
	sysUp := readSystemUptime(ctx, exec)
	if out.HrneoVersion != "" {
		out.HrneoUptime = daemonUptime(ctx, exec, sysUp, "hrneo")
	}
	out.AwgmgrUptime = daemonUptime(ctx, exec, sysUp, "awg-manager")
	return out, nil
}

// EncodeVersionAudit serialises for transport via wire.CommandResult.Output.
func EncodeVersionAudit(va wire.VersionAudit) (string, error) {
	b, err := json.Marshal(va)
	return string(b), err
}

// opkgHrneoVersion reads `opkg info hrneo` and extracts the version, stripping
// the trailing `-N` packager-revision suffix (`2.4.0-1` → `2.4.0`).
func opkgHrneoVersion(ctx context.Context, exec ExecFunc) (string, error) {
	out, err := exec(ctx, "opkg", "info", "hrneo")
	if err != nil {
		return "", fmt.Errorf("opkg info hrneo: %w", err)
	}
	return parseHrneoOpkg(string(out))
}

func parseHrneoOpkg(s string) (string, error) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if !strings.HasPrefix(line, "Version:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "Version:"))
		// Strip packager revision suffix `-N` if present.
		if idx := strings.LastIndex(v, "-"); idx > 0 {
			suffix := v[idx+1:]
			isNumeric := suffix != ""
			for _, r := range suffix {
				if r < '0' || r > '9' {
					isNumeric = false
					break
				}
			}
			if isNumeric {
				v = v[:idx]
			}
		}
		if v == "" {
			return "", fmt.Errorf("empty Version: line in opkg output")
		}
		return v, nil
	}
	return "", fmt.Errorf("no Version: line in opkg output")
}

// readSystemUptime reads /proc/uptime and returns the first field (system
// uptime in seconds). Returns 0 on error — callers should treat 0 as
// "uptime unknown" and skip the daemon-uptime computation.
func readSystemUptime(ctx context.Context, exec ExecFunc) float64 {
	out, err := exec(ctx, "cat", "/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0
	}
	var v float64
	if _, err := fmt.Sscanf(fields[0], "%f", &v); err != nil {
		return 0
	}
	return v
}

// userHZ is the kernel jiffies-per-second constant. Hard-coded to 100, which
// is universal on aarch64 Linux. busybox `getconf` is unavailable on
// Keenetic + Entware, so we cannot read it at runtime.
const userHZ = 100

// daemonUptime returns a humanised uptime for the named daemon, or empty
// string if the daemon is not running / parsing fails. Never returns an
// error — uptime is a "nice to have" cell in the panel.
func daemonUptime(ctx context.Context, exec ExecFunc, sysUp float64, name string) string {
	if sysUp <= 0 {
		return ""
	}
	pidB, err := exec(ctx, "pidof", name)
	if err != nil {
		return ""
	}
	pid := strings.TrimSpace(strings.SplitN(string(pidB), " ", 2)[0])
	if pid == "" {
		return ""
	}
	statB, err := exec(ctx, "cat", "/proc/"+pid+"/stat")
	if err != nil {
		return ""
	}
	starttime, ok := parseProcStatStarttime(string(statB))
	if !ok {
		return ""
	}
	daemonUpSec := int64(sysUp) - (starttime / userHZ)
	if daemonUpSec < 0 {
		return ""
	}
	return humanizeUptime(daemonUpSec)
}

// parseProcStatStarttime extracts field 22 (starttime in jiffies since boot)
// from /proc/$pid/stat. The process name is wrapped in parens and may itself
// contain spaces, so we split on the LAST `)` and tokenise the trailing fields.
func parseProcStatStarttime(s string) (int64, bool) {
	rp := strings.LastIndex(s, ")")
	if rp < 0 || rp+1 >= len(s) {
		return 0, false
	}
	rest := strings.Fields(s[rp+1:])
	// After `)` the fields are: state(1) ppid(2) pgrp(3) session(4) tty(5)
	// tpgid(6) flags(7) minflt(8) cminflt(9) majflt(10) cmajflt(11) utime(12)
	// stime(13) cutime(14) cstime(15) priority(16) nice(17) num_threads(18)
	// itrealvalue(19) starttime(20) ...
	// Index into rest[] is 0-based, so starttime is at index 19.
	if len(rest) < 20 {
		return 0, false
	}
	var v int64
	if _, err := fmt.Sscanf(rest[19], "%d", &v); err != nil {
		return 0, false
	}
	return v, true
}

// humanizeUptime turns a duration in seconds into a Russian short form:
// "3д 4ч" / "5ч 30м" / "1м 23с" / "42с" / "0с".
func humanizeUptime(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%dс", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dм %dс", sec/60, sec%60)
	}
	if sec < 86400 {
		return fmt.Sprintf("%dч %dм", sec/3600, (sec%3600)/60)
	}
	return fmt.Sprintf("%dд %dч", sec/86400, (sec%86400)/3600)
}
