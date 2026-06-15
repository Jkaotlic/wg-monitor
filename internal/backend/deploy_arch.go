package backend

import (
	"fmt"
	"strings"
)

func normalizeAgentDeployArch(raw string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "":
		return "", nil
	case "aarch64", "arm64", "linux-arm64":
		return "arm64", nil
	case "mips", "mipsel", "mipsle", "linux-mips", "linux-mipsel", "linux-mipsle":
		return "mipsle", nil
	default:
		return "", fmt.Errorf("unsupported arch %q (supported: arm64, mipsle)", strings.TrimSpace(raw))
	}
}
