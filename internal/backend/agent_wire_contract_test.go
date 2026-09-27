package backend

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TestAgentCheckConstructorsPassBackendIntake держит провод между агентом и
// бэкендом: всё, что строят конструкторы проверок агента, должно пройти
// canonicalizeReportedChecks. Иначе бэкенд отвергает весь отчёт вместе с
// пульсом и роутер выглядит отвалившимся (ревью v0.46: статус "unknown").
func TestAgentCheckConstructorsPassBackendIntake(t *testing.T) {
	start := time.Now()
	built := []wire.Check{
		checks.OK("dns", start, map[string]any{"endpoints": 2}),
		checks.Fail("tunnel_awg10", start, "no handshake", nil),
		checks.Unverified("hydraroute", start, "routes unreadable", nil),
	}
	if msg, ok := canonicalizeReportedChecks(built); !ok {
		t.Fatalf("backend rejects agent-built checks: %s", msg)
	}
	if !checkUnverified(built[2]) {
		t.Fatalf("backend does not recognise agent's unverified flag: %#v", built[2].Details)
	}
	if checkUnverified(built[0]) {
		t.Fatalf("plain ok must not read as unverified")
	}
}
