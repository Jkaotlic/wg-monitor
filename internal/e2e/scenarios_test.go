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
	if s.Traffic.CarrierTunnelID != carrierID || s.Traffic.CarrierAlive {
		t.Errorf("carrier на экране: %+v, хотим %s и carrier_alive=false", s.Traffic, carrierID)
	}
	if s.Router.Status != "alert" {
		t.Errorf("статус роутера на экране %q, хотим alert", s.Router.Status)
	}
	if len(s.Incidents) != 1 || s.Incidents[0].CheckName != "tunnel_"+carrierID {
		t.Errorf("инциденты на экране: %+v", s.Incidents)
	}
	// Без автопочинки (Task 4 ещё впереди) цепочка не трогается.
	wantNoPromotions(t, r)
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
	alerts := r.Alerts()
	if len(alerts) != 2 {
		t.Fatalf("после порога должно уйти сообщение о восстановлении: %q", alerts)
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
		if len(alerts) != 2 || !alertAbout(alerts, "vpn-backup-one") || !alertAbout(alerts, "vpn-backup-two") || alertAbout(alerts, "vpn-main") {
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
