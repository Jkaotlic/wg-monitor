package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Роутер замолчал. Первая тревога об этом собирается отдельно
// (FormatRouterOffline) и говорит «Роутер не на связи», а напоминание и
// восстановление шли общим шаблоном: «Проверка agent_heartbeat падает»,
// «агент не прислал подробностей» (он не прислал ничего -- роутер молчит),
// «Проверка agent_heartbeat снова в норме». Все три письма об одном событии
// обязаны говорить одинаково и по-человечески.
func TestHeartbeatAlertsSpeakLikeRouterOffline(t *testing.T) {
	chk := wire.Check{Name: "agent_heartbeat", Status: "fail", Details: map[string]any{}}
	texts := map[string]string{
		"тревога": FormatHard(HardArgs{Nickname: "vasya", CheckName: "agent_heartbeat",
			HardSince: time.Now().Add(-time.Hour), ConsecFails: 3, Check: chk}),
		"напоминание": FormatRealert(RealertArgs{Nickname: "vasya", CheckName: "agent_heartbeat",
			HardSince: time.Now().Add(-3 * time.Hour), RealertCount: 2, Check: chk}),
		"восстановление": FormatRecovery(RecoveryArgs{Nickname: "vasya", CheckName: "agent_heartbeat",
			HardSince: time.Now().Add(-2 * time.Hour), RecoveredAt: time.Now()}),
	}
	for what, text := range texts {
		for _, bad := range []string{"agent_heartbeat", "не прислал подробностей"} {
			if strings.Contains(text, bad) {
				t.Errorf("%s: владелец читает %q:\n%s", what, bad, text)
			}
		}
	}
	for _, what := range []string{"тревога", "напоминание"} {
		if !strings.Contains(texts[what], "Роутер не на связи") {
			t.Errorf("%s говорит не так, как первая тревога о молчащем роутере:\n%s", what, texts[what])
		}
		if !strings.Contains(texts[what], "включён ли роутер") {
			t.Errorf("%s не говорит, что проверить самому:\n%s", what, texts[what])
		}
	}
	if !strings.Contains(texts["восстановление"], "Роутер снова на связи") {
		t.Errorf("восстановление не говорит, что роутер вернулся:\n%s", texts["восстановление"])
	}
}
