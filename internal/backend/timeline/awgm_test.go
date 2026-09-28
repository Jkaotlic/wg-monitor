package timeline

import (
	"reflect"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func run(tunnel string, from, to time.Duration, fails int, down bool) db.PingRunRow {
	return db.PingRunRow{TunnelID: tunnel, From: base.Add(from), To: base.Add(to), Fails: fails, WentDown: down, Recovered: true}
}

func TestAwgmRunAnnotatesOverlappingIncident(t *testing.T) {
	got := FoldWithAwgm([]db.EventRow{
		row("tunnel_awg11", "fail", 5*time.Minute),
		row("tunnel_awg11", "ok", 9*time.Minute),
	}, []db.PingRunRow{run("awg11", 3*time.Minute, 8*time.Minute, 5, true)}, AwgmCoverage{}, base.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	inc := got[0]
	if !inc.AwgmFirstFail.Equal(base.Add(3*time.Minute)) || inc.AwgmFails != 5 || !inc.AwgmWentDown || inc.AwgmOnly {
		t.Fatalf("incident = %+v", inc)
	}
}

// Моргнул между нашими отчётами: наша проверка не видела, awg-manager видел.
func TestAwgmOnlyBlinkBecomesQuietIncident(t *testing.T) {
	got := FoldWithAwgm(nil, []db.PingRunRow{
		run("awg11", 0, 2*time.Minute, 3, true),
		run("awg11", 10*time.Minute, 11*time.Minute, 4, true),
		run("awg11", 40*time.Minute, 41*time.Minute, 1, false), // ниже порога -- не в ленту
	}, AwgmCoverage{}, base.Add(2*time.Hour))
	if len(got) != 1 {
		t.Fatalf("хотим одно склеенное моргание: %+v", got)
	}
	inc := got[0]
	if !inc.AwgmOnly || inc.Flaps != 2 || inc.AwgmFails != 7 || inc.Ongoing || inc.CheckName != "tunnel_awg11" {
		t.Fatalf("incident = %+v", inc)
	}
}

func TestAwgmCleanNeedsCoverageAndTunnel(t *testing.T) {
	rows := []db.EventRow{row("tunnel_awg11", "fail", 5*time.Minute), row("tunnel_awg11", "ok", 6*time.Minute)}
	now := base.Add(time.Hour)
	cases := []struct {
		cov  AwgmCoverage
		want bool
	}{
		{AwgmCoverage{Since: base, Until: now, Tunnels: map[string]bool{"awg11": true}}, true},
		{AwgmCoverage{Since: base, Until: now, Tunnels: map[string]bool{}}, false},                                    // пингчек у VPN-туннеля выключен
		{AwgmCoverage{Since: base.Add(10 * time.Minute), Until: now, Tunnels: map[string]bool{"awg11": true}}, false}, // журнал читаем позже
		// P2: агент откатился на версию без ping_log (например, на v0.46) --
		// факт больше не обновляется, Until застыл до происшествия. Молчать о
		// покрытии здесь обязательно: несвежий Until не должен сойти за «сейчас».
		{AwgmCoverage{Since: base, Until: base.Add(time.Minute), Tunnels: map[string]bool{"awg11": true}}, false},
		{AwgmCoverage{}, false}, // старый агент
	}
	for i, c := range cases {
		got := FoldWithAwgm(rows, nil, c.cov, now)
		if got[0].AwgmClean != c.want {
			t.Errorf("случай %d: clean=%v, хотим %v", i, got[0].AwgmClean, c.want)
		}
	}
}

func TestFoldWithAwgmWithoutRunsEqualsFold(t *testing.T) {
	rows := []db.EventRow{row("dns", "fail", 5*time.Minute), row("dns", "ok", 9*time.Minute), row("dns", "fail", 50*time.Minute)}
	now := base.Add(2 * time.Hour)
	if a, b := Fold(rows, now), FoldWithAwgm(rows, nil, AwgmCoverage{}, now); !reflect.DeepEqual(a, b) {
		t.Fatalf("без серий лента обязана совпасть с прежней:\n%+v\n%+v", a, b)
	}
}
