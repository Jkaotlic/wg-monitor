package retention

import (
	"testing"
	"time"
)

// Обслуживание базы запускалось через шесть секунд после КАЖДОГО старта.
// «Раз в неделю» в конфиге при этом читалось как «раз в неделю И на каждый
// рестарт»: четыре выкатки за час -- четыре полных прохода по базе на 1.6 ГБ,
// лежащей на SD-карте Raspberry Pi. Пока они идут, роутер отвечает на всё
// медленнее, чем внешний релей готов ждать.
func TestFirstDelay_UnknownLastRunStartsSoon(t *testing.T) {
	now := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	got := firstDelay(time.Time{}, 168*time.Hour, 6*time.Second, now)
	if got != 6*time.Second {
		t.Fatalf("без записи о прошлом проходе ждали 6s, получили %v", got)
	}
}

func TestFirstDelay_RecentRunWaitsOutTheInterval(t *testing.T) {
	now := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	lastRun := now.Add(-time.Hour)
	got := firstDelay(lastRun, 168*time.Hour, 6*time.Second, now)
	want := 167 * time.Hour
	if got != want {
		t.Fatalf("после свежего прохода ждали %v, получили %v", want, got)
	}
}

func TestFirstDelay_OverdueRunStartsAfterTheUsualDelay(t *testing.T) {
	now := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	lastRun := now.Add(-200 * time.Hour)
	got := firstDelay(lastRun, 168*time.Hour, 6*time.Second, now)
	if got != 6*time.Second {
		t.Fatalf("просроченный проход должен пойти после обычной задержки, получили %v", got)
	}
}

// Задержка после старта нужна и просроченному проходу: сразу после запуска
// бэкенд занят собой -- миграции, первые отчёты агентов, -- и добавлять к
// этому тяжёлый проход по базе незачем.
func TestFirstDelay_NeverReturnsZero(t *testing.T) {
	now := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	if got := firstDelay(now, 0, 6*time.Second, now); got != 6*time.Second {
		t.Fatalf("нулевой интервал: ждали 6s, получили %v", got)
	}
	if got := firstDelay(now.Add(-time.Second), time.Second, 6*time.Second, now); got != 6*time.Second {
		t.Fatalf("ждали 6s, получили %v", got)
	}
}
