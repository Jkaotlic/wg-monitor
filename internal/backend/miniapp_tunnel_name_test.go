package backend

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Кнопка «Починить» в приложении знает только имя проверки. Имя VPN-туннеля
// поднимается из последних событий этого роутера -- тем же путём, каким его
// видит экран VPN-туннелей. Чужой роутер и незнакомая проверка имени не дают:
// подставить в личку чужое имя хуже, чем идентификатор.
func TestMiniappTunnelNameForCheck(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	mine, _ := d.Users().Insert("мой", "4040404040404040404040404040404040404040404040404040404040404040", "198.51.100.11", "awg0")
	other, _ := d.Users().Insert("чужой", "5050505050505050505050505050505050505050505050505050505050505050", "198.51.100.12", "awg0")
	if err := d.Events().Insert(mine, "tunnel_awg12", "fail", `{"tunnel_id":"awg12","tunnel_name":"Дача"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(other, "tunnel_awg13", "fail", `{"tunnel_id":"awg13","tunnel_name":"Чужая дача"}`, time.Now()); err != nil {
		t.Fatal(err)
	}
	deps := Deps{DB: d}

	if got := miniappTunnelNameForCheck(deps, mine, "tunnel_awg12"); got != "Дача" {
		t.Fatalf("своя проверка: имя %q, ждали «Дача»", got)
	}
	if got := miniappTunnelNameForCheck(deps, mine, "tunnel_awg13"); got != "" {
		t.Fatalf("имя с чужого роутера: %q", got)
	}
	if got := miniappTunnelNameForCheck(deps, mine, "tunnel_awg99"); got != "" {
		t.Fatalf("имя для незнакомой проверки: %q", got)
	}
}
