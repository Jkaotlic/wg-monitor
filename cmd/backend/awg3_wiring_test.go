package main

import (
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
)

func TestAwg3StoreNextToSelfHosted(t *testing.T) {
	if got := awg3StorePath(selfhostedamnezia.Config{}); got != "/var/lib/wg-monitor/awg3-panels.json" {
		t.Fatalf("по умолчанию: %s", got)
	}
	custom := filepath.Join(t.TempDir(), "state", "amnezia-selfhosted.json")
	if got := awg3StorePath(selfhostedamnezia.Config{StorePath: custom}); got != filepath.Join(filepath.Dir(custom), "awg3-panels.json") {
		t.Fatalf("рядом с заданным: %s", got)
	}
}
