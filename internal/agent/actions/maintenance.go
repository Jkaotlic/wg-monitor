package actions

import (
	"context"
	"fmt"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}
