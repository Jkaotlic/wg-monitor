package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Лесенка доказывает ступень замером выхода по VPN-туннелю (exit_ip_probe
// tunnel_id): поддельный агент обязан отвечать настоящим wire.ExitProbe, где
// выход через туннель отличается от прямого.
func TestSandboxExitProbe(t *testing.T) {
	out := sandboxOutput(1, "exit_ip_probe", map[string]any{"tunnel_id": "awg12"})
	var p wire.ExitProbe
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("ответ не wire.ExitProbe: %v: %s", err, out)
	}
	if !strings.HasPrefix(p.VPNIP, "203.0.113.") || !strings.HasPrefix(p.DirectIP, "198.51.100.") ||
		p.Changed == nil || !*p.Changed || p.Err != "" {
		t.Fatalf("замер: %+v", p)
	}
}

// Ступени 2-3 кладут конфиг В ТОТ ЖЕ VPN-туннель (target_id): новый туннель
// не заводится, а ответ -- строкой агента «заменён (id=…)».
func TestSandboxImportIntoTarget(t *testing.T) {
	before := len(routerState(1).imported)
	out := sandboxOutput(1, "tunnel_import", map[string]any{"name": "amnezia_nl", "target_id": "awg12", "replace": true})
	if !strings.Contains(out, "заменён (id=awg12)") {
		t.Fatalf("ответ импорта на место: %q", out)
	}
	if after := len(routerState(1).imported); after != before {
		t.Fatalf("импорт на место завёл новый VPN-туннель: %d -> %d", before, after)
	}
	if out := sandboxOutput(1, "tunnel_restart", map[string]any{"tunnel_id": "awg12"}); out == "" {
		t.Fatal("перезапуск без ответа")
	}
}

// sandbox-broken засеян под лесенку: упавший awg12 с происхождением
// (amnezia, nl) и включённой автопочинкой, VPN-туннель со своего сервера под
// именем awg3-панели и запасной VPN-туннель в том же наборе правил.
func TestSandboxSeedForAutorepair(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ids, err := seed(d, 4242)
	if err != nil {
		t.Fatal(err)
	}
	uid := ids["sandbox-broken"]
	if o, ok, _ := d.TunnelOrigins().Get(uid, "awg12"); !ok || o.Provider != "amnezia" || o.Variant != "nl" {
		t.Fatalf("происхождение awg12: %+v", o)
	}
	if s, ok, _ := d.TunnelRepairSettings().Get(uid, "awg12"); !ok || !s.Enabled || s.Provider != "amnezia" {
		t.Fatalf("автопочинка awg12: %+v", s)
	}
	rows, err := d.Events().LatestEventsByPrefixSince(uid, "tunnel_", time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	awg3Name := awg3panel.TunnelName("main", "awg1")
	found := false
	for _, r := range rows {
		if r.CheckName == "tunnel_"+sandboxAwg3TunnelID && strings.Contains(r.DetailsJSON, awg3Name) {
			found = true
		}
	}
	if !found {
		t.Fatalf("нет VPN-туннеля со своего сервера «%s» в событиях", awg3Name)
	}

	snap := routeSnapshot(routerState(uid))
	var pol wire.RoutePolicySummary
	if len(snap.Policies) > 0 {
		pol = snap.Policies[0]
	}
	links := map[string]bool{}
	for _, i := range pol.Interfaces {
		links[i.TunnelID] = i.Available
	}
	if !links["awg10"] {
		t.Fatalf("запасного VPN-туннеля в наборе правил нет: %+v", pol.Interfaces)
	}
	if _, ok := links[sandboxAwg3TunnelID]; !ok {
		t.Fatalf("VPN-туннель своего сервера не в наборе правил: %+v", pol.Interfaces)
	}
	// У прочих роутеров снимок прежний.
	for _, i := range routeSnapshot(routerState(ids["sandbox-home"])).Policies[0].Interfaces {
		if i.TunnelID == sandboxAwg3TunnelID {
			t.Fatal("VPN-туннель своего сервера просочился в снимок другого роутера")
		}
	}
}

// Песочница v0.55: у sandbox-broken есть строка dns_ru, а sandbox-car спит.
func TestSandboxSeedDNSRuAndSleeper(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ids, err := seed(d, 4242)
	if err != nil {
		t.Fatal(err)
	}
	row, ok, err := d.Events().LatestEvent(ids["sandbox-broken"], "dns_ru")
	if err != nil || !ok || row.Status != "fail" || !strings.Contains(row.DetailsJSON, `"ru_upstreams":2`) {
		t.Fatalf("dns_ru у sandbox-broken: %+v %v %v", row, ok, err)
	}
	u, err := d.Users().GetByID(ids["sandbox-car"])
	if err != nil || time.Since(*u.LastSeenAt) < time.Hour {
		t.Fatalf("sandbox-car должен спать (давний отчёт): %+v %v", u, err)
	}
}
