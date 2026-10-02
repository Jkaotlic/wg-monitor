package main

import (
	"strings"
	"testing"
)

// HydraRoute Neo у роутеров песочницы свой: остановка на одном не трогает
// другой, а «Запустить» возвращает его в строй.
func TestSandboxHRNeoIsPerRouter(t *testing.T) {
	const stopped, running int64 = 9001, 9002
	setHRNeoRunning(stopped, false)
	t.Cleanup(func() { setHRNeoRunning(stopped, true) })

	if out := sandboxOutput(stopped, "hrneo_inventory", nil); !strings.Contains(out, `"running":false`) {
		t.Fatalf("остановленный роутер отвечает запущенным: %s", out)
	}
	if out := sandboxOutput(running, "hrneo_inventory", nil); !strings.Contains(out, `"running":true`) {
		t.Fatalf("остановка одного роутера остановила другой: %s", out)
	}
	sandboxOutput(stopped, "service_restart", map[string]any{"name": "hrneo_start"})
	if out := sandboxOutput(stopped, "hrneo_inventory", nil); !strings.Contains(out, `"running":true`) {
		t.Fatalf("после hrneo_start роутер всё ещё остановлен: %s", out)
	}
	sandboxOutput(running, "service_restart", map[string]any{"name": "hrneo_stop"})
	t.Cleanup(func() { setHRNeoRunning(running, true) })
	if out := sandboxOutput(stopped, "hrneo_inventory", nil); !strings.Contains(out, `"running":true`) {
		t.Fatalf("остановка второго роутера задела первый: %s", out)
	}
}
