package linkrepair

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
)

// M-1 + T1: таблица сроков лесенки -- копия сроков агента (сам срок
// действия плюс внеплановый отчёт после действий с туннелем). Разъедутся --
// выданная в последний момент команда ответит, когда починка уже не ждёт,
// и человек увидит «Роутер не ответил вовремя» при сработавшем действии.
func TestAgentBudgetMatchesAgent(t *testing.T) {
	for _, action := range []string{"tunnel_restart", "tunnel_import", "route_policy_promote", "route_status"} {
		if got, want := agentBudget(action), actions.AnswerBudget(action); got != want {
			t.Errorf("agentBudget(%q) = %v, агент отвечает за %v", action, got, want)
		}
		wait := Deps{}.awaitStep()
		if life := queueLife(action, wait); life+actions.AnswerBudget(action) > wait {
			t.Errorf("%s: очередь %v + ответ %v > ожидание %v", action, life, actions.AnswerBudget(action), wait)
		}
	}
}
