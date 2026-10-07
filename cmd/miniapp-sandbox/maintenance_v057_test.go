package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Смена порта песочницы: ручная копия держит установку, замена её уносит и
// называет, куда.
func TestSandboxPorthopLegacyFlow(t *testing.T) {
	sandboxV057Mu.Lock()
	sandboxPorthopOn, sandboxLegacyGone = false, false
	sandboxV057Mu.Unlock()

	status, out, _ := sandboxResult(1, "porthop_install", map[string]any{})
	if status != "err" || !strings.HasPrefix(out, "legacy_running:") {
		t.Fatalf("установка поверх ручной копии: %s %q", status, out)
	}
	status, out, _ = sandboxResult(1, "porthop_install", map[string]any{"replace_legacy": true})
	var st wire.PorthopStatus
	if status != "ok" || json.Unmarshal([]byte(out), &st) != nil || !st.Installed || st.Legacy.MovedTo == "" {
		t.Fatalf("замена: %s %q", status, out)
	}
}

func TestSandboxDNSResetCarriesProbes(t *testing.T) {
	status, out, payload := sandboxResult(1, "dns_reset", map[string]any{"dry_run": true})
	var res wire.DNSResetResult
	if status != "ok" || !strings.HasPrefix(out, "Предпросмотр") || json.Unmarshal(payload, &res) != nil || len(res.Probes) != 3 {
		t.Fatalf("предпросмотр: %s %q %s", status, out, payload)
	}
}

func TestSandboxCleanFreesSpace(t *testing.T) {
	before := sandboxSpace().FreeKB
	sandboxResult(1, "entware_clean_run", nil)
	if after := sandboxSpace().FreeKB; after <= before {
		t.Fatalf("место не прибавилось: %d -> %d", before, after)
	}
}
