package linkrepair

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
)

func TestScenarioFor_Tunnel(t *testing.T) {
	sc, ok := ScenarioFor("tunnel_awg12")
	if !ok {
		t.Fatal("для упавшего туннеля сценарий обязан быть")
	}
	if sc.TunnelID != "awg12" {
		t.Fatalf("TunnelID = %q, хотим awg12", sc.TunnelID)
	}
	// Между уводом на резерв и возвратом лежит чеклист мастера замены как
	// есть: перевыпуск -- это он и есть, дублировать его нельзя.
	want := []string{
		StepFailover,
		replace.StepIssue, replace.StepImport, replace.StepHandshake,
		replace.StepPromote, replace.StepVerify, replace.StepRetire,
		StepFailback,
	}
	if len(sc.Steps) != len(want) {
		t.Fatalf("шагов %d, хотим %d", len(sc.Steps), len(want))
	}
	for i, name := range want {
		if sc.Steps[i].Name != name {
			t.Fatalf("шаг %d = %q, хотим %q", i, sc.Steps[i].Name, name)
		}
		if sc.Steps[i].Status != provision.StepPending {
			t.Fatalf("шаг %q должен стартовать pending", name)
		}
	}
}

// Чинить нечем -- значит сценария нет. Изобразить деятельность хуже, чем
// честно сказать «это не лечится отсюда».
func TestScenarioFor_Unfixable(t *testing.T) {
	for _, name := range []string{"external_reach", "agent_heartbeat"} {
		if _, ok := ScenarioFor(name); ok {
			t.Fatalf("%s: сценария быть не должно", name)
		}
	}
}

func TestScenarioFor_UnknownCheck(t *testing.T) {
	if _, ok := ScenarioFor("нечто_неизвестное"); ok {
		t.Fatal("незнакомая проверка не получает сценарий")
	}
}
