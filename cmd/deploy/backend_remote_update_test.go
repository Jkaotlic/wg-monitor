package main

import (
	"strings"
	"testing"
)

func TestBackendUpdateTimerUsesLessAggressiveInterval(t *testing.T) {
	got := backendUpdateTimer("wg-monitor-backend-update.service")
	if !strings.Contains(got, "OnUnitActiveSec=2min") {
		t.Fatalf("timer should poll at 2min interval, got:\n%s", got)
	}
	if strings.Contains(got, "OnUnitActiveSec=20s") {
		t.Fatalf("timer must not keep the old 20s tight loop:\n%s", got)
	}
}

func TestDecodeBackendHealthVersion(t *testing.T) {
	got, err := decodeBackendHealthVersion(strings.NewReader(`{"version":"v0.13.0-rc132"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "v0.13.0-rc132" {
		t.Fatalf("version = %q", got)
	}
}

func TestDecodeBackendHealthVersionRejectsOversizeResponse(t *testing.T) {
	_, err := decodeBackendHealthVersion(strings.NewReader(strings.Repeat("A", int(maxBackendHealthResponseBytes)+1)))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want oversize response error, got %v", err)
	}
}
