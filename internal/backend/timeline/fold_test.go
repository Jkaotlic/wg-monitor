package timeline

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

var base = time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC)

func row(check, status string, offset time.Duration) db.EventRow {
	return db.EventRow{CheckName: check, Status: status, TS: base.Add(offset)}
}

func TestFoldClosedPair(t *testing.T) {
	got := Fold([]db.EventRow{
		row("dns", "ok", 0),
		row("dns", "fail", 5*time.Minute),
		row("dns", "fail", 6*time.Minute),
		row("dns", "ok", 9*time.Minute),
	}, base.Add(time.Hour))

	if len(got) != 1 {
		t.Fatalf("хотим одно происшествие, получили %+v", got)
	}
	if got[0].DownSec != 240 {
		t.Errorf("длилось %d с, хотим 240", got[0].DownSec)
	}
	if got[0].Ongoing || got[0].Flaps != 1 {
		t.Errorf("одна поломка не идёт и не моргает: %+v", got[0])
	}
}

// Незакрытая пара -- это «идёт прямо сейчас», и её длительность считается до
// текущего момента. Записать ей конец значило бы соврать, что всё прошло.
func TestFoldOngoing(t *testing.T) {
	got := Fold([]db.EventRow{
		row("hydraroute", "fail", 0),
	}, base.Add(10*time.Minute))

	if len(got) != 1 || !got[0].Ongoing {
		t.Fatalf("хотим одно идущее происшествие, получили %+v", got)
	}
	if !got[0].To.IsZero() {
		t.Errorf("у идущего происшествия конца нет, получили %v", got[0].To)
	}
	if got[0].DownSec != 600 {
		t.Errorf("идёт %d с, хотим 600", got[0].DownSec)
	}
}

// Двенадцать морганий подряд -- это ОДНА новость «линия неустойчива», а не
// двенадцать строк. Суммарная длительность считается по падениям: между
// морганиями связь работала, и приписывать эти минуты поломке нельзя.
func TestFoldMergesFlapping(t *testing.T) {
	var rows []db.EventRow
	for i := 0; i < 12; i++ {
		start := time.Duration(i) * 5 * time.Minute
		rows = append(rows,
			row("hydraroute", "fail", start),
			row("hydraroute", "ok", start+time.Minute),
		)
	}

	got := Fold(rows, base.Add(2*time.Hour))
	if len(got) != 1 {
		t.Fatalf("хотим одно слитое происшествие, получили %d", len(got))
	}
	if got[0].Flaps != 12 {
		t.Errorf("моргало %d раз, хотим 12", got[0].Flaps)
	}
	if got[0].DownSec != 12*60 {
		t.Errorf("суммарно лежало %d с, хотим 720", got[0].DownSec)
	}
}

// Два отвала, разделённые тишиной длиннее FlapGap, -- это две разные новости.
// Слить их значило бы спрятать от человека, что упало дважды.
func TestFoldKeepsDistantOutagesApart(t *testing.T) {
	got := Fold([]db.EventRow{
		row("external_reach", "fail", 0),
		row("external_reach", "ok", time.Minute),
		row("external_reach", "fail", 3*time.Hour),
		row("external_reach", "ok", 3*time.Hour+time.Minute),
	}, base.Add(4*time.Hour))

	if len(got) != 2 {
		t.Fatalf("хотим два происшествия, получили %+v", got)
	}
	if !got[0].From.After(got[1].From) {
		t.Errorf("наружу идут свежие вперёд, получили %v затем %v", got[0].From, got[1].From)
	}
}

// Разные проверки не смешиваются: dns и hydraroute -- разные поломки, и пара
// ищется внутри одного имени.
func TestFoldSeparatesChecks(t *testing.T) {
	got := Fold([]db.EventRow{
		row("dns", "fail", 0),
		row("hydraroute", "fail", time.Minute),
		row("dns", "ok", 2*time.Minute),
		row("hydraroute", "ok", 3*time.Minute),
	}, base.Add(time.Hour))

	if len(got) != 2 {
		t.Fatalf("хотим два происшествия, получили %+v", got)
	}
}

// "unknown" открывает происшествие наравне с "fail": неизвестное состояние --
// это ответ «не отвечает», а не спокойный фон. То же решение уже принято на
// клиенте (множество PROBLEM в events.js).
func TestFoldUnknownOpensIncident(t *testing.T) {
	got := Fold([]db.EventRow{
		row("awg_manager", "unknown", 0),
		row("awg_manager", "ok", 2*time.Minute),
	}, base.Add(time.Hour))

	if len(got) != 1 || got[0].DownSec != 120 {
		t.Fatalf("unknown обязан открыть происшествие: %+v", got)
	}
}

func TestFoldQuietWeekIsEmptyNotError(t *testing.T) {
	got := Fold([]db.EventRow{
		row("dns", "ok", 0),
		row("dns", "ok", time.Minute),
	}, base.Add(time.Hour))

	if len(got) != 0 {
		t.Fatalf("спокойная неделя -- это ноль происшествий, получили %+v", got)
	}
}

func TestFoldCapsAtMaxIncidents(t *testing.T) {
	var rows []db.EventRow
	// Разносим поломки на час друг от друга, чтобы они не слились.
	for i := 0; i < MaxIncidents+20; i++ {
		start := time.Duration(i) * time.Hour
		rows = append(rows,
			row("dns", "fail", start),
			row("dns", "ok", start+time.Minute),
		)
	}

	got := Fold(rows, base.Add(time.Duration(MaxIncidents+40)*time.Hour))
	if len(got) != MaxIncidents {
		t.Fatalf("потолок %d, получили %d", MaxIncidents, len(got))
	}
	// Обрезаем старое, а не свежее: человека интересует, что было недавно.
	if !got[0].From.After(got[len(got)-1].From) {
		t.Errorf("первым обязано идти свежее происшествие")
	}
}

func TestFoldDoesNotMutateInput(t *testing.T) {
	rows := []db.EventRow{
		row("dns", "ok", 2*time.Minute),
		row("dns", "fail", 0),
	}
	Fold(rows, base.Add(time.Hour))
	if rows[0].Status != "ok" || !rows[0].TS.Equal(base.Add(2*time.Minute)) {
		t.Errorf("вход изменён: %+v", rows)
	}
}
