package alerts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Строка bypass_leak, какой её пишет бэкенд при поднятой тревоге (v0.56,
// спека A): несущий VPN-туннель и ответ доступности сервисов того же отчёта.
func bypassLeakDetails(reach string) map[string]any {
	return map[string]any{
		"tunnel_id": "awg12", "tunnel_name": "Франкфурт", "observed": "mismatch",
		"exit_at": "2026-10-06T12:00:00Z", "external_reach": reach,
	}
}

func bypassLeakHard(reach string, ns []NeighborSummary) string {
	return FormatHard(HardArgs{
		Nickname: "дача", CheckName: "bypass_leak", HardSince: time.Now(), ConsecFails: 2,
		Check:     wire.Check{Name: "bypass_leak", Status: "fail", Details: bypassLeakDetails(reach)},
		Neighbors: ns,
	})
}

// latinOutside -- латиница вне ёлочек, кроме «VPN».
func latinOutside(text string) []string {
	var bad []string
	depth := 0
	var word strings.Builder
	flush := func() {
		if w := word.String(); w != "" && w != "VPN" {
			bad = append(bad, w)
		}
		word.Reset()
	}
	for _, r := range text {
		switch {
		case r == '«':
			flush()
			depth++
		case r == '»':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			word.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return bad
}

func TestBypassLeakAlertTexts(t *testing.T) {
	const (
		headline = "VPN-туннель «Франкфурт» работает, но не меняет ваш адрес"
		body     = "Роутер отправляет через этот VPN-туннель сайты из ваших правил, но в интернете вы выходите с тем же адресом, что и без него. Заблокированные сайты, скорее всего, не откроются. Если всё открывается — это особенность вашего VPN-сервера, делать ничего не нужно."
		advice   = "Проверьте VPN-туннель на экране роутера и при необходимости замените конфиг."
	)
	got := bypassLeakHard("ok", nil)
	for _, want := range []string{headline, body, advice} {
		if !strings.Contains(got, want) {
			t.Errorf("нет %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bypass_leak") || strings.Contains(got, "без ответа") {
		t.Errorf("имя проверки или «без ответа» в тексте:\n%s", got)
	}
	if strings.Contains(got, "Запасной") || strings.Contains(got, "запасной") {
		t.Errorf("резерв обещан без живого соседа:\n%s", got)
	}
	if bad := latinOutside(got); len(bad) > 0 {
		t.Errorf("латиница вне ёлочек %v:\n%s", bad, got)
	}
	assertSaysVPNTunnel(t, "тревога", got)
}

func TestBypassLeakAlertColour(t *testing.T) {
	if got := bypassLeakHard("ok", nil); !strings.HasPrefix(got, "🟡") {
		t.Errorf("сервисы открываются -- жёлтый:\n%s", got)
	}
	if got := bypassLeakHard("", nil); !strings.HasPrefix(got, "🟡") {
		t.Errorf("доступность не проверялась -- жёлтый:\n%s", got)
	}
	if got := bypassLeakHard("fail", nil); !strings.HasPrefix(got, "🔴") {
		t.Errorf("сервисы не открываются -- красный:\n%s", got)
	}
}

func TestBypassLeakAlertSpareOnlyWhenLive(t *testing.T) {
	alive := []NeighborSummary{{CheckName: "tunnel_awg10", TunnelName: "Амстердам", Status: "alive"}}
	dead := []NeighborSummary{{CheckName: "tunnel_awg10", TunnelName: "Амстердам", Status: "dead"}}
	if got := bypassLeakHard("ok", alive); !strings.Contains(got, "Запасной VPN-туннель «Амстердам» на связи") {
		t.Errorf("живой резерв не назван:\n%s", got)
	}
	if got := bypassLeakHard("ok", dead); strings.Contains(got, "Амстердам") {
		t.Errorf("мёртвый резерв назван:\n%s", got)
	}
}

func TestBypassLeakRealertAndRecovery(t *testing.T) {
	chk := wire.Check{Name: "bypass_leak", Status: "fail", Details: bypassLeakDetails("ok")}
	realert := FormatRealert(RealertArgs{Nickname: "дача", CheckName: "bypass_leak", HardSince: time.Now(), RealertCount: 1, Check: chk})
	if !strings.Contains(realert, "работает, но не меняет ваш адрес") {
		t.Errorf("напоминание без заголовка:\n%s", realert)
	}
	rec := FormatRecovery(RecoveryArgs{
		Nickname: "дача", CheckName: "bypass_leak", HardSince: time.Now().Add(-time.Hour), RecoveredAt: time.Now(),
		Check: wire.Check{Name: "bypass_leak", Status: "ok", Details: map[string]any{"tunnel_name": "Франкфурт", "observed": "match"}},
	})
	if !strings.Contains(rec, "VPN-туннель «Франкфурт» снова меняет ваш адрес") {
		t.Errorf("восстановление:\n%s", rec)
	}
	for name, text := range map[string]string{"напоминание": realert, "восстановление": rec} {
		if strings.Contains(text, "bypass_leak") {
			t.Errorf("%s: имя проверки в тексте:\n%s", name, text)
		}
		if bad := latinOutside(text); len(bad) > 0 {
			t.Errorf("%s: латиница вне ёлочек %v:\n%s", name, bad, text)
		}
		assertSaysVPNTunnel(t, name, text)
	}
}

// Диспетчер берёт соседей для bypass_leak из VPN-туннелей роутера, кроме
// несущего: про запасной -- только когда он жив.
func TestDispatcher_BypassLeakNamesLiveSpareOnly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		spare     string
		wantSpare bool
	}{
		{"резерв жив", "ok", true},
		{"резерв лежит", "fail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDB(t)
			uid, _ := d.Users().Insert("router-a", "6767000000000000000000000000000000000000000000000000000000000000", "198.51.100.1", "awg0")
			now := time.Now().UTC()
			if err := d.Events().Insert(uid, "tunnel_awg12", "ok", `{"tunnel_name":"Франкфурт"}`, now); err != nil {
				t.Fatal(err)
			}
			if err := d.Events().Insert(uid, "tunnel_awg10", tc.spare, `{"tunnel_name":"Амстердам"}`, now); err != nil {
				t.Fatal(err)
			}
			disp := NewDispatcher(d, &fakeTG{}, Config{FailThreshold: 3, RecoveryThreshold: 2})
			disp.SetNotifySink(&recordingSink{delivers: 1})
			if err := disp.Handle(context.Background(), uid, "router-a", "bypass_leak", hardTr(),
				chk("bypass_leak", "fail", bypassLeakDetails("ok"))); err != nil {
				t.Fatal(err)
			}
			text, _, ok := disp.Last(uid, "bypass_leak")
			if !ok {
				t.Fatal("тревога не ушла")
			}
			if got := strings.Contains(text, "Запасной VPN-туннель «Амстердам» на связи"); got != tc.wantSpare {
				t.Fatalf("резерв назван=%v, want %v:\n%s", got, tc.wantSpare, text)
			}
			if strings.Contains(text, "Запасной VPN-туннель «Франкфурт»") {
				t.Fatalf("несущий назван запасным:\n%s", text)
			}
		})
	}
}

func TestBypassLeakNeighborExclude(t *testing.T) {
	if got := NeighborExclude("bypass_leak", map[string]any{"tunnel_id": "awg12"}); got != "tunnel_awg12" {
		t.Fatalf("bypass_leak: %q", got)
	}
	if got := NeighborExclude("tunnel_awg11", nil); got != "tunnel_awg11" {
		t.Fatalf("tunnel: %q", got)
	}
}
