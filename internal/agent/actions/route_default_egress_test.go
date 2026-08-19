package actions

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Маршрут по умолчанию -- ФАКТ, который знает только awg-manager
// (settings.download.routeTag). Вывести его из флагов defaultRoute нельзя:
// на живом домашнем роутере он стоит у всех трёх туннелей сразу, а трафик
// при этом уходит напрямую. Экран, считающий дефолт по флагам, скажет
// "всё остальное идёт через awg11" -- и это будет неправдой.
func TestBuildRouteSnapshot_DefaultEgress(t *testing.T) {
	tunnels := awgmgr.TunnelsAll{Tunnels: []awgmgr.Tunnel{
		{ID: "awg10", Name: "main", InterfaceName: "opkgtun10", Enabled: true, DefaultRoute: true},
		{ID: "awg11", Name: "work", InterfaceName: "opkgtun11", Enabled: true, DefaultRoute: true},
	}}

	cases := []struct {
		name            string
		activeDefaultID string
		want            string
	}{
		// routeTag="direct" -- это не туннель, которого мы не нашли, а прямое
		// утверждение роутера: трафик по умолчанию идёт мимо туннелей.
		{"роутер говорит direct", "direct", wire.DefaultEgressDirect},
		{"роутер называет туннель", "awg11", "awg11"},
		// Пустой routeTag -- роутер не сказал. Подставить сюда первый попавшийся
		// туннель с флагом значило бы выдать догадку за факт.
		{"роутер промолчал", "", ""},
		// Туннель, которого нет в снимке, назвать дефолтом нельзя: экран
		// сослался бы на линию, которую не показывает.
		{"назван неизвестный туннель", "awg99", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := buildRouteSnapshot(nil, &tunnels, nil, nil, nil, tc.activeDefaultID, nil, nil, false)
			if snap.DefaultEgress != tc.want {
				t.Fatalf("DefaultEgress = %q, want %q", snap.DefaultEgress, tc.want)
			}
		})
	}
}

// Флаги на туннелях остаются как есть: они описывают настройку туннеля, и
// перетирать их выводом о дефолте было бы потерей данных.
func TestBuildRouteSnapshot_DefaultEgressLeavesFlagsAlone(t *testing.T) {
	tunnels := awgmgr.TunnelsAll{Tunnels: []awgmgr.Tunnel{
		{ID: "awg10", InterfaceName: "opkgtun10", Enabled: true, DefaultRoute: true},
		{ID: "awg11", InterfaceName: "opkgtun11", Enabled: true, DefaultRoute: true},
	}}
	snap := buildRouteSnapshot(nil, &tunnels, nil, nil, nil, "direct", nil, nil, false)
	if snap.DefaultEgress != wire.DefaultEgressDirect {
		t.Fatalf("DefaultEgress = %q", snap.DefaultEgress)
	}
	for _, tun := range snap.Tunnels {
		if !tun.DefaultRoute {
			t.Fatalf("флаг default_route у %s потерян", tun.ID)
		}
	}
}
