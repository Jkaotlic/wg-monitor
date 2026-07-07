package backend

import (
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/provision"
)

func TestBuildReviveScriptRewritesBackendURLAndRestarts(t *testing.T) {
	s := buildReviveScript("https://wg.snekhaev.example")
	for _, want := range []string{
		"NEW='https://wg.snekhaev.example'",
		"/opt/etc/wg-monitor/config.yaml",
		"bak-revive-",
		"/^backend:[ \\t]*$/", // section-aware
		"/opt/etc/init.d/S99wg-monitor restart",
		provision.StepMarker + " " + provision.StepBackendURLRewrite,
		provision.StepMarker + " " + provision.StepServiceRestarted,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("revive script missing %q:\n%s", want, s)
		}
	}

	// The repair-repoint job checklist is terminal_connected (relay-emitted,
	// Task 7) -> backend_url_rewritten -> service_restarted -> verify_online
	// (engine, runner.go). The script itself must emit its two markers in
	// that order, straddling the config rewrite and the agent restart
	// respectively — otherwise the dashboard's checklist stalls forever on a
	// step nothing ever completes (see runner.go's onLine: a step only
	// advances once its marker line arrives).
	mvIdx := strings.Index(s, `mv "$CFG.tmp" "$CFG"`)
	rewriteMarkerIdx := strings.Index(s, provision.StepMarker+" "+provision.StepBackendURLRewrite)
	restartIdx := strings.Index(s, "/opt/etc/init.d/S99wg-monitor restart")
	restartMarkerIdx := strings.Index(s, provision.StepMarker+" "+provision.StepServiceRestarted)
	if mvIdx < 0 || rewriteMarkerIdx < 0 || restartIdx < 0 || restartMarkerIdx < 0 {
		t.Fatalf("script missing one of the expected markers/lines:\n%s", s)
	}
	if !(mvIdx < rewriteMarkerIdx && rewriteMarkerIdx < restartIdx && restartIdx < restartMarkerIdx) {
		t.Fatalf("step markers out of order: mv=%d rewriteMarker=%d restart=%d restartMarker=%d\n%s",
			mvIdx, rewriteMarkerIdx, restartIdx, restartMarkerIdx, s)
	}
}
