package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// failThreshold -- боевой порог автомата (state.fail_threshold по умолчанию):
// столько подряд провалов до Hard.
const failThreshold = 3

// wantNoAlerts -- людям ничего не ушло.
func wantNoAlerts(t *testing.T, r *rig) {
	t.Helper()
	if got := r.Alerts(); len(got) != 0 {
		t.Fatalf("ложная тревога, ушло людям: %q", got)
	}
}

// wantNoPromotions -- цепочка политики не тронута.
func wantNoPromotions(t *testing.T, r *rig) {
	t.Helper()
	if got := r.awgm.Promotions(); len(got) != 0 {
		t.Fatalf("трафик уводили: %+v", got)
	}
}

func alertAbout(alerts []string, substr string) bool {
	for _, a := range alerts {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
}

// S1. Форма разбора 02.11: несущий обменивается ключами (рукопожатие 17 с), а
// проба через него падает. Агент говорит fail + matrix_ok=false, и бэкенд не
// смеет перекрасить это в ok: тревога после порога, на экране несущий не живой.
func TestDeadCarrierFreshHandshake(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	wantNoAlerts(t, r)
	s := r.Screen()
	if s.Traffic.CarrierTunnelID != carrierID || !s.Traffic.CarrierAlive || s.Traffic.Mode != "split" {
		t.Fatalf("живой роутер: несущий должен быть %s и жив, экран %+v", carrierID, s.Traffic)
	}

	r.awgm.SetMatrix(carrierID, false)
	r.Ticks(failThreshold - 1)
	wantNoAlerts(t, r) // порог ещё не пройден
	if got := r.Screen().Tunnel(t, carrierID).Status; got != "fail" {
		t.Fatalf("уже первый провал должен быть виден на экране, статус %q", got)
	}

	r.Tick()
	r.DrainCommands()
	alerts := r.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("после порога ровно одна тревога, ушло: %q", alerts)
	}
	if !strings.Contains(alerts[0], "vpn-main") {
		t.Errorf("тревога не называет туннель по имени: %q", alerts[0])
	}
	if strings.Contains(alerts[0], carrierID) {
		t.Errorf("в тревоге id туннеля (alerts-speak-to-owner): %q", alerts[0])
	}
	s = r.Screen()
	if got := s.Tunnel(t, carrierID).Status; got != "fail" {
		t.Errorf("мёртвый несущий на экране со статусом %q", got)
	}
	// Экран -- из отчёта, снятого до увода: несущий назван и не живой.
	if s.Traffic.CarrierTunnelID != carrierID || s.Traffic.CarrierAlive {
		t.Errorf("carrier на экране: %+v, хотим %s и carrier_alive=false", s.Traffic, carrierID)
	}
	if s.Router.Status != "alert" {
		t.Errorf("статус роутера на экране %q, хотим alert", s.Router.Status)
	}
	if len(s.Incidents) != 1 || s.Incidents[0].CheckName != "tunnel_"+carrierID {
		t.Errorf("инциденты на экране: %+v", s.Incidents)
	}
}

var standardChain = []string{"Wireguard2", "OpkgTun10", "OpkgTun11"}

func wantChain(t *testing.T, r *rig, want ...string) {
	t.Helper()
	if got := r.awgm.PolicyLinks(policyHR); !slices.Equal(got, want) {
		t.Fatalf("порядок цепочки %v, хотим %v", got, want)
	}
}

// kill -- проба через туннель падает, и туннель не поднимется сам.
func kill(r *rig, id string) { r.awgm.SetMatrix(id, false) }

// heal -- туннель снова живой: проба проходит, ключами обменялся только что.
func heal(r *rig, id string) {
	r.awgm.SetMatrix(id, true)
	r.awgm.UpdateTunnel(id, func(tu *fakeTunnel) {
		tu.Status, tu.StatusDetails, tu.HandshakeAge = "running", "", freshAge
	})
}

// S2. Резерв простаивает: обмен ключами устарел, но проба через него жива. Он
// живой и годен в резерв; будить человека не за что.
func TestIdleReserveAliveByProbe(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Ticks(failThreshold + 2)
	wantNoAlerts(t, r)
	wantNoPromotions(t, r)
	s := r.Screen()
	for _, id := range []string{backup1ID, backup2ID} {
		if got := s.Tunnel(t, id).Status; got != "ok" {
			t.Errorf("простаивающий резерв %s на экране %q", id, got)
		}
	}
	if !slices.Equal(s.Traffic.ReserveTunnelIDs, []string{backup1ID, backup2ID}) {
		t.Errorf("живые резервы на экране: %v", s.Traffic.ReserveTunnelIDs)
	}
	if !s.Traffic.CarrierAlive {
		t.Errorf("несущий жив, а экран говорит иначе: %+v", s.Traffic)
	}
	if note := s.Tunnel(t, carrierID).Note; note != "" {
		t.Errorf("у несущего со свежим обменом ключами пометка: %q", note)
	}
	if note := s.Tunnel(t, backup1ID).Note; !strings.Contains(note, "простоя") {
		t.Errorf("простаивающий резерв не объясняет, почему жив: %q", note)
	}
}

// S3. Отказала цель пробы: не отвечает никто. Туннели не виноваты -- ни тревог,
// ни увода, даже при включённой автопочинке.
func TestProbeTargetDownEverywhere(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	r.EnableAutorepair(carrierID)
	for _, id := range []string{carrierID, backup1ID, backup2ID} {
		kill(r, id)
	}
	r.Ticks(failThreshold + 2)
	r.DrainCommands()
	wantNoAlerts(t, r)
	wantNoPromotions(t, r)
	wantChain(t, r, standardChain...)
}

// S4. Флап ниже порога: проба падает на 1-2 обхода и оживает -- ни тревоги, ни
// увода, сколько бы раз это ни повторилось.
func TestFlapBelowThreshold(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	r.EnableAutorepair(carrierID)
	for range 4 {
		kill(r, carrierID)
		r.Ticks(failThreshold - 1)
		heal(r, carrierID)
		r.Tick()
	}
	r.DrainCommands()
	wantNoAlerts(t, r)
	wantNoPromotions(t, r)
	if got := r.Screen().Tunnel(t, carrierID).Status; got != "ok" {
		t.Fatalf("после оживления статус %q", got)
	}
}

// S5. Резерв мёртв: человеку сказать, а несущий и порядок звеньев не трогать.
func TestDeadReserve(t *testing.T) {
	check := func(t *testing.T, r *rig) {
		t.Helper()
		wantNoPromotions(t, r)
		wantChain(t, r, standardChain...)
		alerts := r.Alerts()
		if len(alerts) == 0 || !strings.Contains(alerts[0], "vpn-backup-one") {
			t.Fatalf("нет тревоги про резерв: %q", alerts)
		}
		wantReserveAlert(t, r.tg.Log()[0])
		s := r.Screen()
		if got := s.Tunnel(t, backup1ID).Status; got != "fail" {
			t.Errorf("мёртвый резерв на экране %q", got)
		}
		if s.Traffic.CarrierTunnelID != carrierID || !s.Traffic.CarrierAlive {
			t.Errorf("несущий жив и не менялся, экран: %+v", s.Traffic)
		}
		if slices.Contains(s.Traffic.ReserveTunnelIDs, backup1ID) {
			t.Errorf("мёртвый резерв назван живым: %v", s.Traffic.ReserveTunnelIDs)
		}
		if !s.Router.ReserveOnlyAlert {
			t.Errorf("тревога только по резерву, а reserve_only_alert=false: %+v", s.Router)
		}
	}
	t.Run("автопочинка выключена", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		check(t, r)
		if got := r.awgm.Calls(); len(got) != 0 {
			t.Fatalf("при выключенной автопочинке роутер трогали: %v", got)
		}
	})
	t.Run("автопочинка резерва включена", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.EnableAutorepair(backup1ID)
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		check(t, r)
		if got := r.awgm.Calls(); !slices.Equal(got, []string{"restart:" + backup1ID}) {
			t.Fatalf("упал резерв: только перезапуск его самого, вышло %v", got)
		}
	})
}

// S6. Восстановление: после порога выздоровления люди узнают, экран снова ok.
func TestRecoveryAfterDeadCarrier(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	kill(r, carrierID)
	r.Ticks(failThreshold)
	r.DrainCommands()
	if n := len(r.Alerts()); n != 1 {
		t.Fatalf("тревога одна, ушло %d: %q", n, r.Alerts())
	}
	heal(r, carrierID)
	r.Tick() // порог выздоровления -- 2 обхода
	if n := len(r.Alerts()); n != 1 {
		t.Fatalf("выздоровление раньше порога: %q", r.Alerts())
	}
	if len(r.Screen().Incidents) != 1 {
		t.Fatalf("инцидент закрылся раньше порога")
	}
	r.Tick()
	r.DrainCommands()
	alerts := r.Alerts()
	if len(alerts) != 3 {
		t.Fatalf("после порога: восстановление и возврат трафика (S14), ушло: %q", alerts)
	}
	if !strings.Contains(alerts[1], "vpn-main") {
		t.Errorf("восстановление не называет туннель: %q", alerts[1])
	}
	if strings.Contains(alerts[1], carrierID) {
		t.Errorf("в сообщении id туннеля: %q", alerts[1])
	}
	s := r.Screen()
	if got := s.Tunnel(t, carrierID).Status; got != "ok" {
		t.Errorf("статус на экране %q", got)
	}
	r.Tick() // несущий на экране -- из отчёта уже после возврата
	s = r.Screen()
	if !s.Traffic.CarrierAlive || s.Router.Status == "alert" || len(s.Incidents) != 0 {
		t.Errorf("экран после восстановления: роутер %+v, несущий %+v, инциденты %+v", s.Router, s.Traffic, s.Incidents)
	}
}

// S7. Автопочинка включена: увод, перезапуск, возврат -- только после
// доказательства, что перезапуск помог.
func TestAutorepairMovesRestartsAndReturns(t *testing.T) {
	t.Run("перезапуск помог", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.EnableAutorepair(carrierID)
		r.awgm.OnRestart(func(id string) { heal(r, id) })
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		want := []string{"promote:" + backup1ID, "restart:" + carrierID, "promote:" + carrierID}
		if got := r.awgm.Calls(); !slices.Equal(got, want) {
			t.Fatalf("порядок действий %v, хотим %v", got, want)
		}
		wantChain(t, r, standardChain...)
		r.Ticks(2) // порог выздоровления
		r.DrainCommands()
		s := r.Screen()
		if got := s.Tunnel(t, carrierID).Status; got != "ok" || !s.Traffic.CarrierAlive || len(s.Incidents) != 0 {
			t.Errorf("экран после починки: статус %q, %+v, инциденты %+v", got, s.Traffic, s.Incidents)
		}
		alerts := r.Alerts()
		if len(alerts) != 1 {
			t.Fatalf("ход починки дописывается в саму тревогу, а ушло: %q", alerts)
		}
		if strings.Contains(alerts[0], carrierID) || strings.Contains(alerts[0], backup1ID) {
			t.Errorf("в тексте id туннеля: %q", alerts[0])
		}
		if !strings.Contains(alerts[0], "VPN-туннель") {
			t.Errorf("в тексте нет полной формы «VPN-туннель»: %q", alerts[0])
		}
	})
	t.Run("перезапуск не помог", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.EnableAutorepair(carrierID)
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		// Трафик остаётся на резерве: назад на сломанный не возвращаем.
		want := []string{"promote:" + backup1ID, "restart:" + carrierID}
		if got := r.awgm.Calls(); !slices.Equal(got, want) {
			t.Fatalf("порядок действий %v, хотим %v", got, want)
		}
		wantChain(t, r, "OpkgTun10", "Wireguard2", "OpkgTun11")
		alerts := r.Alerts()
		if len(alerts) == 0 || !alertAbout(alerts, "vpn-backup-one") {
			t.Fatalf("человеку не сказано, куда ушёл трафик: %q", alerts)
		}
		r.Tick()
		s := r.Screen()
		if s.Traffic.CarrierTunnelID != backup1ID || !s.Traffic.CarrierAlive {
			t.Errorf("после увода несущий -- резерв, и он жив; экран: %+v", s.Traffic)
		}
	})
}

// S8. Старый агент (нет matrix_ok) и ложный fail при выключенном ping-check:
// защита нормализатора осталась.
func TestOldAgentFalseFailStaysOK(t *testing.T) {
	r := newRig(t, rigOpts{AgentVersion: "v0.40.0"})
	for i := range failThreshold + 2 {
		r.PostReport(wire.Report{
			Timestamp:    time.Now().UTC().Add(time.Duration(i) * time.Millisecond),
			AgentVersion: "v0.40.0",
			Checks: []wire.Check{
				{Name: "tunnels", Status: "ok", Details: map[string]any{"tunnel_count": 1}},
				{Name: "tunnel_" + carrierID, Status: "fail", Details: map[string]any{
					"tunnel_id": carrierID, "tunnel_name": "vpn-main", "status": "running", "enabled": true,
					"ping_check_status": "disabled", "ping_check_fail_count": 0, "handshake_age_sec": 17,
					"address_conflict": false, "error": "pingCheck=disabled",
				}},
				{Name: "agent_heartbeat", Status: "ok"},
			},
		})
	}
	wantNoAlerts(t, r)
	if got := r.Screen().Tunnel(t, carrierID); got.Status != "ok" {
		t.Fatalf("ложный fail старого агента дошёл до экрана: %+v", got)
	}
}

// S9. awg-manager без матрицы (старше 2.18): проверка не падает, и ложных
// тревог нет. Настоящий отказ по-прежнему виден по рукопожатию.
func TestNoMatrixEndpoint(t *testing.T) {
	t.Run("все обмениваются ключами", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.awgm.SetMatrixMissing(true)
		for _, id := range []string{backup1ID, backup2ID} {
			r.awgm.UpdateTunnel(id, func(tu *fakeTunnel) { tu.HandshakeAge = freshAge })
		}
		r.Ticks(failThreshold + 2)
		wantNoAlerts(t, r)
		s := r.Screen()
		if got := s.Check("tunnels"); got != "ok" {
			t.Fatalf("инвентарь при 404 матрицы: %q", got)
		}
		if !s.Traffic.CarrierAlive || s.Traffic.CarrierTunnelID != carrierID {
			t.Fatalf("несущий: %+v", s.Traffic)
		}
	})
	// ИЗВЕСТНЫЙ ДЕФЕКТ B2 (docs/superpowers/plans/2026-11-02-v064-bugs.md): без
	// матрицы единственный признак резерва -- рукопожатие, и простой неотличим
	// от отказа. Сейчас на такого резерва уходит тревога «не отвечает»; ждём
	// решения, что хуже на старых сборках -- ложная тревога или пропущенный
	// мёртвый резерв. Тест фиксирует нынешнее поведение: ложь только про резервы,
	// несущий не назван мёртвым и трафик никуда не уводится.
	t.Run("резерв простаивает: дефект B2", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.awgm.SetMatrixMissing(true)
		r.Ticks(failThreshold + 2)
		alerts := r.Alerts()
		if len(alerts) != 2 || !alertAbout(alerts, "vpn-backup-one") || !alertAbout(alerts, "vpn-backup-two") || alertAbout(alerts, "«vpn-main» не отвечает") {
			t.Fatalf("B2 изменился, пересмотрите запись о дефекте: %q", alerts)
		}
		wantNoPromotions(t, r)
		if s := r.Screen(); !s.Traffic.CarrierAlive || s.Traffic.CarrierTunnelID != carrierID {
			t.Fatalf("несущий: %+v", s.Traffic)
		}
	})
	t.Run("несущий без рукопожатия виден", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.awgm.SetMatrixMissing(true)
		for _, id := range []string{backup1ID, backup2ID} {
			r.awgm.UpdateTunnel(id, func(tu *fakeTunnel) { tu.HandshakeAge = freshAge })
		}
		r.Tick()
		r.awgm.UpdateTunnel(carrierID, func(tu *fakeTunnel) { tu.HandshakeAge = idleAge })
		r.Ticks(failThreshold)
		if a := r.Alerts(); len(a) != 1 || !strings.Contains(a[0], "vpn-main") {
			t.Fatalf("настоящий отказ без матрицы: %q", a)
		}
	})
}

// S10. Перезагрузка роутера: сначала awg-manager не отвечает, потом туннели
// поднимаются с опозданием -- DNS ещё нет, «endpoint resolve failed» (журнал
// 02.11). Ниже порога это не авария; держится дольше порога -- уже авария.
func TestRouterRebootLateTunnels(t *testing.T) {
	boot := func(r *rig) {
		r.Tick()
		r.killAgent()
		r.awgm.SetDown(true)
		r.reviveAgent("")
		r.Tick() // агент проснулся раньше awg-manager
		r.awgm.SetDown(false)
		for _, id := range []string{carrierID, backup1ID, backup2ID} {
			r.awgm.ClearMatrix(id)
			r.awgm.UpdateTunnel(id, func(tu *fakeTunnel) {
				tu.Status, tu.StatusDetails, tu.HandshakeAge = "broken", "endpoint resolve failed", neverHandshake
			})
		}
	}
	up := func(r *rig) {
		for _, id := range []string{carrierID, backup1ID, backup2ID} {
			heal(r, id)
		}
	}
	t.Run("успел в окно", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		boot(r)
		r.Tick() // туннели ещё поднимаются
		up(r)
		r.Ticks(failThreshold + 1)
		wantNoAlerts(t, r)
		wantNoPromotions(t, r)
		if s := r.Screen(); !s.Traffic.CarrierAlive || s.Router.Status == "alert" {
			t.Fatalf("после подъёма: %+v, %+v", s.Router, s.Traffic)
		}
	})
	t.Run("не поднялись за порог", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		boot(r)
		r.Ticks(failThreshold)
		alerts := r.Alerts()
		if len(alerts) == 0 {
			t.Fatal("туннели лежат дольше порога, а тревоги нет")
		}
		s := r.Screen()
		if s.Traffic.CarrierAlive {
			t.Fatalf("несущий лежит, экран говорит «жив»: %+v", s.Traffic)
		}
		up(r)
		before := len(alerts)
		r.Ticks(2)
		if got := r.Alerts(); len(got) <= before {
			t.Fatalf("нет сообщения о восстановлении: %q", got)
		}
	})
}

// S11. awg-manager молчит: это авария самого демона, а не трёх туннелей разом.
// Экран не должен рисовать «всё работает».
func TestAwgManagerSilent(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	r.awgm.SetDown(true)
	r.Ticks(failThreshold)
	r.DrainCommands()
	alerts := r.Alerts()
	if len(alerts) == 0 {
		t.Fatal("awg-manager молчит, а людям ни слова")
	}
	for _, a := range alerts {
		for _, id := range []string{carrierID, backup1ID, backup2ID} {
			if strings.Contains(a, id) {
				t.Errorf("в тексте id туннеля: %q", a)
			}
		}
	}
	wantNoPromotions(t, r)
	if alertAbout(alerts, "http://") || alertAbout(alerts, "awgmgr") {
		t.Errorf("сырая ошибка с адресом в тревоге владельцу (B6): %q", alerts)
	}
	if alertAbout(alerts, "не установлен") {
		t.Errorf("про молчащий демон сказано, что HydraRoute не установлен: %q", alerts)
	}
	s := r.Screen()
	if got := s.Check("awg_manager"); got != "fail" {
		t.Errorf("проверка awg_manager на экране: %q", got)
	}
	if s.Router.Status != "alert" {
		t.Errorf("роутер на экране %q, хотим alert", s.Router.Status)
	}
	for _, inc := range s.Incidents {
		if strings.HasPrefix(inc.CheckName, "tunnel_") {
			t.Errorf("молчание демона записано как падение туннеля: %+v", inc)
		}
	}
	// Демон вернулся: тревога закрыта.
	r.awgm.SetDown(false)
	r.Ticks(3)
	if s := r.Screen(); len(s.Incidents) != 0 || s.Router.Status == "alert" {
		t.Errorf("после возврата демона: %+v, инциденты %+v", s.Router, s.Incidents)
	}
}

// S12. Агент молчит: роутер offline, а не «всё ок»; последние зелёные проверки
// выдавать за текущие нельзя.
func TestAgentSilent(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	r.killAgent()
	const silence = 2 * time.Hour
	r.AgeLastSeen(silence)
	r.ScanHeartbeat(silence)
	alerts := r.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("про молчание ровно одно сообщение: %q", alerts)
	}
	// Устаревшим экран считает по stale и reach (miniapp/src/staleness.js).
	s := r.Screen()
	if s.Router.Reach != "offline" || !s.Router.Stale {
		t.Errorf("молчащий роутер на экране: %+v", s.Router)
	}
	// Вернулся: сторож закрывает молчание по свежему отчёту.
	r.reviveAgent("")
	r.Tick()
	r.ScanHeartbeat(0)
	s = r.Screen()
	if s.Router.Reach != "online" || s.Router.Stale {
		t.Errorf("вернувшийся роутер всё ещё молчит: %+v", s.Router)
	}
	if got := r.Alerts(); len(got) != 2 {
		t.Errorf("о возвращении на связь -- одно сообщение: %q", got)
	}
}

// wantReserveAlert -- тревога про запасной не звучит как про несущий (B5).
func wantReserveAlert(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "🔴") || strings.Contains(text, "не доходит") {
		t.Errorf("тревога про резерв звучит как авария несущего: %q", text)
	}
	if !strings.Contains(text, "трафик идёт через «vpn-main»") {
		t.Errorf("тревога про резерв не говорит, где идёт трафик: %q", text)
	}
}

// wantCalls -- какие изменения роутер получил, по порядку.
func wantCalls(t *testing.T, r *rig, want ...string) {
	t.Helper()
	if got := r.awgm.Calls(); !slices.Equal(got, want) {
		t.Fatalf("действия на роутере %v, хотим %v", got, want)
	}
}

// deadCarrierMoved -- обход: несущий умер, автопочинка выключена, увод прошёл.
func deadCarrierMoved(t *testing.T, r *rig) {
	t.Helper()
	r.Tick()
	kill(r, carrierID)
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
}

// S13. Д1: автопочинка выключена, несущий мёртв, резерв жив -- ровно один увод,
// без перезапуска и выпуска. Первая версия тревоги не обещает обход (B7), а
// правка говорит про увод как про факт.
func TestMoveOnlyWithoutAutorepair(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	wantChain(t, r, "OpkgTun10", "Wireguard2", "OpkgTun11")
	if got := r.awgm.Promotions(); len(got) != 1 || got[0] != (Promotion{Policy: policyHR, Iface: "OpkgTun10", TunnelID: backup1ID}) {
		t.Fatalf("увод: %+v", got)
	}
	log := r.tg.Log()
	if strings.Contains(log[0], "обход идёт") || strings.Contains(log[0], "Пока ничего") {
		t.Errorf("до подтверждённого увода тревога обещает обход: %q", log[0])
	}
	if !strings.Contains(log[0], "🔴") {
		t.Errorf("обход ещё не уведён, а тревога не красная: %q", log[0])
	}
	alerts := r.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("про увод -- в той же тревоге, ушло: %q", alerts)
	}
	if !strings.Contains(alerts[0], "Увёл трафик на запасной VPN-туннель «vpn-backup-one»") {
		t.Errorf("тревога не говорит про увод: %q", alerts[0])
	}
	if strings.Contains(alerts[0], "они не работают") {
		t.Errorf("после увода тревога всё ещё говорит, что правила не работают: %q", alerts[0])
	}
	for _, id := range []string{carrierID, backup1ID} {
		if strings.Contains(alerts[0], id) {
			t.Errorf("в тексте id туннеля: %q", alerts[0])
		}
	}
	if r.MoveRecord(carrierID) == "" {
		t.Error("записи об уводе нет: возврат потеряется")
	}
	// Дальше несущий лежит, и ничего нового не происходит.
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
	s := r.Screen()
	if s.Traffic.CarrierTunnelID != backup1ID || !s.Traffic.CarrierAlive {
		t.Errorf("после увода несущий -- живой резерв, экран: %+v", s.Traffic)
	}
	// Правила идут за цепочкой: после увода упавший их не несёт (B8), и в списке
	// роутеров это янтарная «резерв не работает», а не красная авария.
	if !s.Router.ReserveOnlyAlert {
		t.Errorf("трафик на живом резерве, а экран зовёт это аварией: %+v", s.Router)
	}
}

// S14. Д2: туннель ожил -- трафик возвращается первым звеном, запись снята.
func TestMoveReturnsOnRecovery(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	heal(r, carrierID)
	r.Tick() // порог выздоровления ещё не пройден
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
	r.Tick()
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID, "promote:"+carrierID)
	wantChain(t, r, standardChain...)
	if got := r.MoveRecord(carrierID); got != "" {
		t.Errorf("запись об уводе осталась: %q", got)
	}
	if !alertAbout(r.Alerts(), "Вернул трафик на VPN-туннель «vpn-main»") {
		t.Errorf("про возврат не сказано: %q", r.Alerts())
	}
	r.Ticks(3)
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID, "promote:"+carrierID) // ровно один возврат
}

// S15. То же, но между уводом и восстановлением бэкенд перезапущен: запись живёт
// в базе, а не в памяти процесса.
func TestMoveReturnsAfterBackendRestart(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	r.restartBackend()
	heal(r, carrierID)
	r.Ticks(2)
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID, "promote:"+carrierID)
	wantChain(t, r, standardChain...)
}

// Бэкенд упал уже после Recovery, не успев вернуть: следующий отчёт с живым
// туннелем доводит возврат (запись пережила рестарт, а Recovery второй раз не
// случится).
func TestMoveReturnsWhenRecoveryAlreadyPassed(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	r.awgm.RefusePermit(true) // первый возврат не проходит
	heal(r, carrierID)
	r.Ticks(2)
	r.DrainCommands()
	if r.MoveRecord(carrierID) == "" {
		t.Fatal("возврат не прошёл, а запись снята: он потерян")
	}
	r.restartBackend()
	r.awgm.RefusePermit(false)
	r.Tick()
	r.DrainCommands()
	wantChain(t, r, standardChain...)
	if got := r.MoveRecord(carrierID); got != "" {
		t.Errorf("запись осталась: %q", got)
	}
}

// S16. Д3: первый резерв мёртв по пробе, второй жив -- увод на второго. Заодно
// доказывает на настоящих данных, что id туннеля из проверок совпал с TunnelID
// звена политики: иначе мёртвый не был бы опознан.
func TestMoveSkipsDeadReserve(t *testing.T) {
	check := func(t *testing.T, r *rig) {
		t.Helper()
		if got := r.awgm.Promotions(); len(got) == 0 || got[0].TunnelID != backup2ID {
			t.Fatalf("увод мимо мёртвого резерва: %+v", got)
		}
		wantChain(t, r, "OpkgTun11", "Wireguard2", "OpkgTun10")
	}
	t.Run("автопочинка выключена", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, carrierID)
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		check(t, r)
		wantCalls(t, r, "promote:"+backup2ID)
	})
	t.Run("автопочинка включена", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.EnableAutorepair(carrierID)
		kill(r, carrierID)
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		check(t, r)
	})
}

// S17. Живых резервов нет -- не трогаем, человеку так и сказано.
func TestNoLiveReserveNoMove(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	for _, id := range []string{carrierID, backup1ID, backup2ID} {
		// Цель пробы жива (иначе виновата она, S3): отвечает посторонний туннель.
		kill(r, id)
	}
	r.awgm.SetTunnel(fakeTunnel{ID: "awg99", Name: "vpn-other", Backend: "kernel", PolicyIface: "OpkgTun99", Iface: "opkgtun99",
		Status: "running", Enabled: true, HandshakeAge: freshAge, PingStatus: "disabled"})
	r.awgm.SetMatrix("awg99", true)
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantNoPromotions(t, r)
	wantChain(t, r, standardChain...)
	if got := r.MoveRecord(carrierID); got != "" {
		t.Errorf("запись об уводе без увода: %q", got)
	}
	if !alertAbout(r.Alerts(), "Живого запасного VPN-туннеля нет") {
		t.Errorf("человеку не сказано, что уводить некуда: %q", r.Alerts())
	}
	if alertAbout(r.Alerts(), "обход идёт") {
		t.Errorf("обещан обход, которого нет: %q", r.Alerts())
	}
}

// S18. Упал резерв при выключенной автопочинке: ни увода, ни записи, и тревога
// не звучит как авария несущего (B5).
func TestDeadReserveNoMoveOff(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	kill(r, backup1ID)
	r.Ticks(failThreshold + 1)
	r.DrainCommands()
	wantCalls(t, r)
	wantChain(t, r, standardChain...)
	if got := r.MoveRecord(backup1ID); got != "" {
		t.Errorf("запись об уводе при падении резерва: %q", got)
	}
	alerts := r.Alerts()
	if len(alerts) != 1 {
		t.Fatalf("одна тревога про резерв, ушло: %q", alerts)
	}
	wantReserveAlert(t, alerts[0])
	// Резерв ожил: обычное «восстановилось», возвращать нечего.
	heal(r, backup1ID)
	r.Ticks(2)
	r.DrainCommands()
	wantCalls(t, r)
}

// S19. Пока туннель лежал, человек сам переставил цепочку. Возврат его решение
// не отменяет: не трогаем, запись снята.
func TestReturnRespectsHumanReorder(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	r.awgm.SetPolicy(policyHR, "OpkgTun11", "Wireguard2", "OpkgTun10")
	heal(r, carrierID)
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
	wantChain(t, r, "OpkgTun11", "Wireguard2", "OpkgTun10")
	if got := r.MoveRecord(carrierID); got != "" {
		t.Errorf("запись не снята: %q", got)
	}
	if alertAbout(r.Alerts(), "Вернул") {
		t.Errorf("сказано про возврат, которого не было: %q", r.Alerts())
	}
}

// S20. Флап: увод и возврат упираются в потолок попыток; серия смертей не гоняет
// перестановки без предела, а трафик остаётся на живом резерве, и об этом сказано.
func TestFlapHitsAttemptsCeiling(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	for range 6 {
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		heal(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
	}
	// Потолок три: увод, возврат, увод; второй возврат уже не разрешён.
	wantCalls(t, r, "promote:"+backup1ID, "promote:"+carrierID, "promote:"+backup1ID)
	wantChain(t, r, "OpkgTun10", "Wireguard2", "OpkgTun11")
	if r.MoveRecord(carrierID) == "" {
		t.Error("запись снята: когда потолок отпустит, вернуть будет некому")
	}
	if !alertAbout(r.Alerts(), "Трафик оставил на запасном VPN-туннеле «vpn-backup-one»") {
		t.Errorf("человеку не сказано, почему трафик не вернулся: %q", r.Alerts())
	}
	s := r.Screen()
	if s.Traffic.CarrierTunnelID != backup1ID || !s.Traffic.CarrierAlive {
		t.Errorf("трафик на резерве, экран: %+v", s.Traffic)
	}
}

// S20б. Потолок общий с лесенкой: окно уже выбрано -- не уводим, и сказано почему.
func TestMoveRefusedAtCeiling(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	for range 3 {
		if err := r.core.LinkRepair.Attempts.Started(e2eRouter, "tunnel_"+carrierID); err != nil {
			t.Fatal(err)
		}
	}
	kill(r, carrierID)
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantCalls(t, r)
	if got := r.MoveRecord(carrierID); got != "" {
		t.Errorf("запись без увода: %q", got)
	}
	if !alertAbout(r.Alerts(), "не увёл") || !alertAbout(r.Alerts(), "Заблокированное сейчас не открывается") {
		t.Errorf("человеку не сказано, почему трафик не уведён: %q", r.Alerts())
	}
}

// S21. Увод не подтверждён: записи нет, и тревога говорит правду.
func TestMissedPromote(t *testing.T) {
	check := func(t *testing.T, r *rig, word string) {
		t.Helper()
		wantNoPromotions(t, r)
		wantChain(t, r, standardChain...)
		if got := r.MoveRecord(carrierID); got != "" {
			t.Errorf("запись об уводе при непрошедшем уводе: %q", got)
		}
		alerts := r.Alerts()
		if !alertAbout(alerts, word) || !alertAbout(alerts, "Заблокированное сейчас не открывается") {
			t.Errorf("тревога не говорит правду про увод: %q", alerts)
		}
		if alertAbout(alerts, "Увёл") || alertAbout(alerts, "Обход блокировок работает") {
			t.Errorf("обещан увод, которого нет: %q", alerts)
		}
	}
	t.Run("роутер отказал", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.awgm.RefusePermit(true)
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		check(t, r, "не дал увести")
		// Туннель ожил: возвращать нечего.
		r.awgm.RefusePermit(false)
		heal(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		wantCalls(t, r)
	})
	t.Run("роутер не ответил", func(t *testing.T) {
		r := newRig(t, rigOpts{AwaitStep: 1500 * time.Millisecond})
		r.Tick()
		release := r.awgm.HangPermit()
		defer release()
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.waitEngineIdle()
		r.settle()
		check(t, r, "не подтвердил")
		release()
		r.DrainCommands()
	})
}

// S22. Туннель первым звеном в двух политиках: уводятся обе, возврат обеих. В
// третьей он резерв -- её не трогаем.
func TestTwoPoliciesMovedAndReturned(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.awgm.SetPolicy("Work", "Wireguard2", "OpkgTun11", "OpkgTun10")
	r.awgm.SetPolicy("Side", "OpkgTun10", "Wireguard2")
	r.Tick()
	kill(r, carrierID)
	r.Ticks(failThreshold)
	r.DrainCommands()
	wantChain(t, r, "OpkgTun10", "Wireguard2", "OpkgTun11")
	if got := r.awgm.PolicyLinks("Work"); !slices.Equal(got, []string{"OpkgTun11", "Wireguard2", "OpkgTun10"}) {
		t.Fatalf("вторая политика: %v", got)
	}
	if got := r.awgm.PolicyLinks("Side"); !slices.Equal(got, []string{"OpkgTun10", "Wireguard2"}) {
		t.Fatalf("политику, где он резерв, тронули: %v", got)
	}
	if got := r.awgm.Promotions(); len(got) != 2 {
		t.Fatalf("уводов: %+v", got)
	}
	if !alertAbout(r.Alerts(), "«vpn-backup-one» и «vpn-backup-two»") {
		t.Errorf("названы не оба запасных: %q", r.Alerts())
	}
	heal(r, carrierID)
	r.Ticks(2)
	r.DrainCommands()
	wantChain(t, r, standardChain...)
	if got := r.awgm.PolicyLinks("Work"); !slices.Equal(got, []string{"Wireguard2", "OpkgTun11", "OpkgTun10"}) {
		t.Fatalf("вторая политика после возврата: %v", got)
	}
	if got := r.awgm.PolicyLinks("Side"); !slices.Equal(got, []string{"OpkgTun10", "Wireguard2"}) {
		t.Fatalf("третья политика после возврата: %v", got)
	}
	if got := r.awgm.Promotions(); len(got) != 4 {
		t.Fatalf("перестановок: %+v", got)
	}
}

// S23. Снимок неполный (сбой чтения) -- это не «человек переставил»: решение
// откладывается, запись остаётся, следующий отчёт возвращает трафик. А вот
// настоящее удаление политики запись снимает.
func TestReturnWaitsForCompleteSnapshot(t *testing.T) {
	for _, path := range []string{"GET /api/routing/access-policies", "GET /api/routing/tunnels", "GET /api/routing/policy-interfaces"} {
		t.Run(path, func(t *testing.T) {
			r := newRig(t, rigOpts{})
			deadCarrierMoved(t, r)
			r.awgm.FailPath(path, true)
			heal(r, carrierID)
			r.Ticks(failThreshold)
			r.DrainCommands()
			wantCalls(t, r, "promote:"+backup1ID)
			if r.MoveRecord(carrierID) == "" {
				t.Fatal("снимок неполный, а запись снята: возврат потерян")
			}
			r.awgm.FailPath(path, false)
			r.Tick()
			r.DrainCommands()
			wantCalls(t, r, "promote:"+backup1ID, "promote:"+carrierID)
			wantChain(t, r, standardChain...)
		})
	}
	t.Run("политику удалили", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.awgm.SetPolicy("Other", "OpkgTun11")
		deadCarrierMoved(t, r)
		r.awgm.RemovePolicy(policyHR)
		heal(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		wantCalls(t, r, "promote:"+backup1ID)
		if got := r.MoveRecord(carrierID); got != "" {
			t.Errorf("политики нет, а запись живёт: %q", got)
		}
	})
}

// S24. Роутер отказывает в возврате: потолок на этом не сгорает, и текста про
// падения нет -- туннель не падал.
func TestRefusedReturnDoesNotBurnCeiling(t *testing.T) {
	r := newRig(t, rigOpts{})
	deadCarrierMoved(t, r)
	r.awgm.RefusePermit(true)
	heal(r, carrierID)
	r.Ticks(failThreshold + 3)
	r.DrainCommands()
	wantChain(t, r, "OpkgTun10", "Wireguard2", "OpkgTun11")
	r.awgm.RefusePermit(false)
	r.Tick()
	r.DrainCommands()
	wantChain(t, r, standardChain...)
	if alertAbout(r.Alerts(), "падает раз за разом") {
		t.Errorf("туннель не падал, а сказано про падения: %q", r.Alerts())
	}
	if !alertAbout(r.Alerts(), "Вернул трафик") {
		t.Errorf("про возврат не сказано: %q", r.Alerts())
	}
}

// S25. На момент Hard снимка нет. FSM нового Hard не даст, поэтому следующий отчёт
// пробует снова, а здоровые туннели лишних команд не получают.
func TestMoveRetriedAfterNoSnapshot(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	kill(r, carrierID)
	r.Ticks(failThreshold - 1)
	r.awgm.FailPath("GET /api/system/hydraroute-status", true) // снимок фатален без неё
	r.Tick()
	r.DrainCommands()
	wantCalls(t, r)
	r.awgm.FailPath("GET /api/system/hydraroute-status", false)
	r.Tick()
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
	if !alertAbout(r.Alerts(), "Увёл трафик") {
		t.Errorf("про увод не сказано: %q", r.Alerts())
	}
	// Больше ничего: туннель уже не первый, команд нет.
	before := r.exch.dispatched.Load()
	r.Ticks(3)
	r.DrainCommands()
	if got := r.exch.dispatched.Load(); got != before {
		t.Errorf("после увода агенту ушло ещё %d команд", got-before)
	}
}

// Здоровые туннели и запасной в hard не получают ни одной команды от повтора.
func TestNoRetryCommandsWithoutNeed(t *testing.T) {
	t.Run("все здоровы", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Ticks(failThreshold + 3)
		r.DrainCommands()
		if got := r.exch.dispatched.Load(); got != 0 {
			t.Errorf("здоровым туннелям ушло %d команд", got)
		}
	})
	t.Run("запасной лежит", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		before := r.exch.dispatched.Load()
		r.Ticks(3)
		r.DrainCommands()
		if got := r.exch.dispatched.Load(); got != before {
			t.Errorf("повтор ради запасного: %d команд", got-before)
		}
	})
	t.Run("живого резерва нет", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, carrierID)
		kill(r, backup1ID)
		kill(r, backup2ID)
		r.awgm.SetTunnel(fakeTunnel{ID: "awg99", Name: "vpn-other", Backend: "kernel", PolicyIface: "OpkgTun99", Iface: "opkgtun99",
			Status: "running", Enabled: true, HandshakeAge: freshAge, PingStatus: "disabled"})
		r.awgm.SetMatrix("awg99", true)
		r.Ticks(failThreshold)
		r.DrainCommands()
		before := r.exch.dispatched.Load()
		r.Ticks(3)
		r.DrainCommands()
		if got := r.exch.dispatched.Load(); got != before {
			t.Errorf("повтор без резерва: %d команд", got-before)
		}
	})
	t.Run("потолок и сказано один раз", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		for range 3 {
			if err := r.core.LinkRepair.Attempts.Started(e2eRouter, "tunnel_"+carrierID); err != nil {
				t.Fatal(err)
			}
		}
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		log, before := len(r.tg.Log()), r.exch.dispatched.Load()
		r.Ticks(3)
		r.DrainCommands()
		if got := r.exch.dispatched.Load(); got != before {
			t.Errorf("на потолке спрашиваем роутер каждый отчёт: %d команд", got-before)
		}
		if got := len(r.tg.Log()); got != log {
			t.Errorf("на потолке спам: %q", r.tg.Log())
		}
	})
}

// S26 и S29. Бэкенд остановлен, пока команда увода у агента: отмена -- не отказ,
// запись остаётся. Новый экземпляр на следующем отчёте доводит увод (цепочка всё
// ещё ведёт в мёртвый).
func TestBackendRestartRightAfterHard(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	release := r.awgm.HangPermit()
	defer release()
	kill(r, carrierID)
	r.Ticks(failThreshold)
	r.waitFor("команда увода у агента", func() bool { return r.awgm.Reads("POST /api/access-policies/permit") > 0 })
	r.stopCtx() // SIGTERM посреди ожидания
	r.waitEngineIdle()
	if r.MoveRecord(carrierID) == "" {
		t.Fatal("отмена посчитана отказом: запись снята, хотя команда могла уйти")
	}
	wantNoPromotions(t, r)
	r.restartBackend()
	release()
	r.Tick()
	r.DrainCommands()
	wantCalls(t, r, "promote:"+backup1ID)
	if r.MoveRecord(carrierID) == "" {
		t.Error("увод прошёл, а записи нет")
	}
	heal(r, carrierID)
	r.Ticks(2)
	r.DrainCommands()
	wantChain(t, r, standardChain...)
}

// S27. Человек сам убрал мёртвый туннель с первого места: повтор не спорит с ним.
func TestRetryRespectsHumanReorder(t *testing.T) {
	r := newRig(t, rigOpts{})
	r.Tick()
	kill(r, carrierID)
	r.Ticks(failThreshold - 1)
	r.awgm.FailPath("GET /api/system/hydraroute-status", true)
	r.Tick()
	r.DrainCommands()
	r.awgm.FailPath("GET /api/system/hydraroute-status", false)
	r.awgm.SetPolicy(policyHR, "OpkgTun11", "Wireguard2", "OpkgTun10")
	before := r.exch.dispatched.Load()
	r.Ticks(3)
	r.DrainCommands()
	wantNoPromotions(t, r)
	wantChain(t, r, "OpkgTun11", "Wireguard2", "OpkgTun10")
	if got := r.exch.dispatched.Load(); got != before {
		t.Errorf("не первый -- команд нет, ушло %d", got-before)
	}
}

// S28. Каскад A->B, B->C: порядок после оживления всех -- исходный, кто бы ни ожил
// первым. Запись первого не теряется, пока трафик стоит ниже по его же цепочке.
func TestCascadeRestoresOriginalOrder(t *testing.T) {
	setup := func(t *testing.T) *rig {
		t.Helper()
		r := newRig(t, rigOpts{})
		deadCarrierMoved(t, r) // A -> B
		r.Tick()
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands() // B -> C
		wantCalls(t, r, "promote:"+backup1ID, "promote:"+backup2ID)
		wantChain(t, r, "OpkgTun11", "OpkgTun10", "Wireguard2")
		return r
	}
	// Каждый обход с доведением фоновой работы: порядок решает логика, а не гонка.
	ticks := func(r *rig, n int) {
		for range n {
			r.Tick()
			r.DrainCommands()
		}
	}
	t.Run("первым ожил A", func(t *testing.T) {
		r := setup(t)
		heal(r, carrierID)
		ticks(r, failThreshold)
		if r.MoveRecord(carrierID) == "" {
			t.Fatal("запись A потеряна как «перестановка»")
		}
		wantChain(t, r, "OpkgTun11", "OpkgTun10", "Wireguard2")
		heal(r, backup1ID)
		ticks(r, failThreshold)
		wantChain(t, r, standardChain...)
		if r.MoveRecord(carrierID) != "" || r.MoveRecord(backup1ID) != "" {
			t.Error("записи остались")
		}
	})
	t.Run("первым ожил B", func(t *testing.T) {
		r := setup(t)
		heal(r, backup1ID)
		ticks(r, failThreshold)
		wantChain(t, r, "OpkgTun10", "OpkgTun11", "Wireguard2")
		heal(r, carrierID)
		ticks(r, failThreshold)
		wantChain(t, r, standardChain...)
		if r.MoveRecord(carrierID) != "" || r.MoveRecord(backup1ID) != "" {
			t.Error("записи остались")
		}
	})
}
