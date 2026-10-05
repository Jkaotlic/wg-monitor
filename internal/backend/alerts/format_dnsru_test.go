package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Отчёт агента v0.55: сервер русских зон молчит, заграничные отвечают.
func dnsRuDownDetails() map[string]any {
	return map[string]any{
		"ru_upstreams": 1, "ru_failed": 1, "router_resolves": true,
		"endpoints_detail": []any{map[string]any{
			"type": "dot", "target": "common.dot.dns.yandex.net:853", "reachable": false, "err": "i/o timeout",
		}},
	}
}

// Спека v0.55, C: своя тревога владельцу словами «Русские сайты (банки,
// госуслуги) могут не открываться: не отвечает сервер имён для русских
// сайтов» и совет.
func TestDNSRuAlertSaysRussianSitesMayNotOpen(t *testing.T) {
	const phrase = "Русские сайты (банки, госуслуги) могут не открываться: не отвечает сервер имён для русских сайтов"
	chk := wire.Check{Name: "dns_ru", Status: "fail", Details: dnsRuDownDetails()}
	texts := map[string]string{
		"тревога": FormatHard(HardArgs{Nickname: "router-a", CheckName: "dns_ru", HardSince: time.Now(), ConsecFails: 3, Check: chk}),
		"напоминание": FormatRealert(RealertArgs{
			Nickname: "router-a", CheckName: "dns_ru", HardSince: time.Now(), RealertCount: 1, Check: chk,
		}),
	}
	for what, got := range texts {
		if !strings.Contains(got, phrase) {
			t.Errorf("%s: нет фразы спеки:\n%s", what, got)
		}
		if !strings.Contains(got, "Что делать") {
			t.Errorf("%s: нет совета:\n%s", what, got)
		}
		if strings.Contains(got, "Проверка dns_ru") || strings.Contains(got, "dns_ru") {
			t.Errorf("%s: имя проверки в тексте для владельца:\n%s", what, got)
		}
		if !strings.Contains(got, "проверка: сервер имён для русских сайтов") {
			t.Errorf("%s: подпись проверки не человеческая:\n%s", what, got)
		}
	}
}

func TestDNSRuRecovery(t *testing.T) {
	since := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	got := FormatRecovery(RecoveryArgs{
		Nickname: "router-a", CheckName: "dns_ru", HardSince: since, RecoveredAt: since.Add(15 * time.Minute),
		Check: wire.Check{Name: "dns_ru", Status: "ok", Details: map[string]any{"ru_upstreams": 1, "ru_failed": 0, "router_resolves": true}},
	})
	if !strings.Contains(got, "Сервер имён для русских сайтов снова отвечает") {
		t.Errorf("нет заголовка восстановления:\n%s", got)
	}
	if !strings.Contains(got, "Простой: 15 мин") || !strings.Contains(got, "🟢") {
		t.Errorf("восстановление не обычное:\n%s", got)
	}
	if strings.Contains(got, "dns_ru") {
		t.Errorf("имя проверки в тексте:\n%s", got)
	}

	// Строка проверки пропала из отчёта (ру-апстримы убрали из настроек) --
	// бэкенд закрывает тревогу сам; «снова отвечает» тут было бы неправдой.
	gone := FormatRecovery(RecoveryArgs{
		Nickname: "router-a", CheckName: "dns_ru", HardSince: since, RecoveredAt: since.Add(15 * time.Minute),
		Check: wire.Check{Name: "dns_ru", Status: "ok", Details: map[string]any{"reason": DNSRuGoneReason}},
	})
	if strings.Contains(gone, "снова отвечает") || !strings.Contains(gone, "больше нет отдельного сервера имён для русских сайтов") {
		t.Errorf("закрытие по пропаже проверки:\n%s", gone)
	}
}

// Пробуждение мобильного роутера перечисляет, что не так, словами.
func TestDNSRuWakeLabel(t *testing.T) {
	if got := wakeCheckLabel(wire.Check{Name: "dns_ru", Status: "fail"}); got != "сервер имён для русских сайтов не отвечает" {
		t.Errorf("wakeCheckLabel = %q", got)
	}
}

// Две русских серверa против одного заграничного роняют и dns (порог 2/3):
// фраза «общая проверка молчит» тогда ложна.
func TestDNSRuDiagnoseDependsOnDNSAlert(t *testing.T) {
	chk := wire.Check{Name: "dns_ru", Status: "fail", Details: dnsRuDownDetails()}
	base := HardArgs{Nickname: "router-a", CheckName: "dns_ru", HardSince: time.Now(), ConsecFails: 3, Check: chk}
	alone := FormatHard(base)
	if !strings.Contains(alone, "общая проверка поиска сайтов молчит") {
		t.Errorf("без аварии dns фраза должна остаться:\n%s", alone)
	}
	base.Check.Details["router_resolves"] = true
	base.Neighbors = []NeighborSummary{{CheckName: "dns", Status: "hard"}}
	both := FormatHard(base)
	if strings.Contains(both, "поиска сайтов молчит") {
		t.Errorf("при аварии dns фраза ложна:\n%s", both)
	}
	for _, lie := range []string{"не отвечают и другие", "не только русские"} {
		if strings.Contains(both, lie) {
			t.Errorf("взаимоисключающая фраза %q:\n%s", lie, both)
		}
	}
	if !strings.Contains(both, "одна поломка, а не две") {
		t.Errorf("нет «одна поломка»:\n%s", both)
	}
	if !strings.Contains(both, "тоже в аварии") {
		t.Errorf("нет правды про обе проверки:\n%s", both)
	}
}
