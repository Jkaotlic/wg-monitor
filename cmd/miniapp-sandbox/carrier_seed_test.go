package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// B1 (v0.56): песочница -- тоже фикстура. Снимок маршрутов поддельного
// агента и засеянные проверки одного роутера обязаны называть один id одним
// именем, а активное звено набора -- одним и тем же туннелем: иначе экраны
// песочницы спорят между собой не из-за кода, а из-за сида (sandbox-work:
// awg10 был «vpn-nl» в проверках и «vpn-de» в снимке).
func TestSandboxSnapshotAgreesWithChecks(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ids, err := seed(d, 4242)
	if err != nil {
		t.Fatal(err)
	}
	for nick, uid := range ids {
		var snap wire.RouteSnapshot
		if err := json.Unmarshal([]byte(sandboxOutput(uid, "route_status", nil)), &snap); err != nil {
			t.Fatalf("%s: снимок не разобрать: %v", nick, err)
		}
		names := map[string]string{}
		for _, tu := range snap.Tunnels {
			names[tu.ID] = tu.Name
		}
		rows, err := d.Events().LatestEventsByPrefixSince(uid, "tunnel_", time.Now().Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			var det struct {
				ID   string `json:"tunnel_id"`
				Name string `json:"tunnel_name"`
			}
			if json.Unmarshal([]byte(r.DetailsJSON), &det) != nil || det.ID == "" {
				continue
			}
			if want, ok := names[det.ID]; ok && want != det.Name {
				t.Errorf("%s: %s в проверке «%s», в снимке маршрутов «%s»", nick, det.ID, det.Name, want)
			}
		}
		hr, ok, err := d.Events().LatestEvent(uid, "hydraroute")
		if err != nil || !ok {
			continue
		}
		var hd struct {
			Policies []wire.PolicyBrief `json:"policies"`
		}
		if json.Unmarshal([]byte(hr.DetailsJSON), &hd) != nil || len(hd.Policies) == 0 {
			continue
		}
		for _, p := range hd.Policies {
			if !p.ViaVPN || p.ActiveTunnelID == "" {
				continue
			}
			found := false
			for _, sp := range snap.Policies {
				if strings.EqualFold(sp.Name, p.Name) {
					found = true
					if sp.ActiveTunnelID != p.ActiveTunnelID {
						t.Errorf("%s: активное звено «%s» в проверке %s, в снимке %s", nick, p.Name, p.ActiveTunnelID, sp.ActiveTunnelID)
					}
				}
			}
			if !found {
				t.Errorf("%s: набора «%s» нет в снимке маршрутов", nick, p.Name)
			}
		}
	}
}
