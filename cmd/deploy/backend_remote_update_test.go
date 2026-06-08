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
