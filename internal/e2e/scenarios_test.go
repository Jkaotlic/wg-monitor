package e2e

import (
	"strings"
	"testing"
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
