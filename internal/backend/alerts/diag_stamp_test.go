package alerts

import (
	"strings"
	"testing"
)

// «Диагностика» отдаёт ПОСЛЕДНИЙ отчёт роутера, а заново проверяет, только
// когда отчёта нет вовсе. На рабочем роутере 11.09.2026 кнопка в 12:08 вернула
// отчёт от 10:10 -- и сводка «всё в порядке» выдавала двухчасовое показание
// за свежее. Время отчёта -- часть ответа, а не подробность.
func TestParseDiagReport_SummaryNamesReportTime(t *testing.T) {
	// Образец снят 2026-09-10T16:59:06+03:00 -- время роутера, в его поясе.
	summary, _, fallback := ParseDiagReport(diagJSON(t, loadDiagFixture(t)))
	if fallback {
		t.Fatal("настоящий отчёт ушёл в сырой вид")
	}
	if !strings.Contains(summary, "10.09") || !strings.Contains(summary, "16:59") {
		t.Fatalf("сводка не говорит, когда снят отчёт: %q", summary)
	}
	if words := ownerLatin(summary); len(words) > 0 {
		t.Errorf("в сводке латиница %v: %q", words, summary)
	}
}

// Отчёт без времени -- не повод падать: сводка просто молчит о нём.
func TestParseDiagReport_SummaryWithoutTimeStillReads(t *testing.T) {
	m := loadDiagFixture(t)
	delete(m, "generatedAt")
	summary, _, fallback := ParseDiagReport(diagJSON(t, m))
	if fallback || !strings.Contains(summary, "всё в порядке") {
		t.Fatalf("без времени отчёт должен читаться как прежде: %q (fallback=%v)", summary, fallback)
	}
	if strings.Contains(summary, "снят") {
		t.Fatalf("время, которого нет, не выдумываем: %q", summary)
	}
}
