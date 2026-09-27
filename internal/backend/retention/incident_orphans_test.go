package retention

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// BUG-02: роутер, молчащий больше недели, не теряет свои инциденты: иначе
// вернувшись, он получает повторную «первую» тревогу, а «починилось» не
// приходит никогда. Удаляется только состояние проверки, которую роутер
// больше не шлёт, ПОКА сам отчитывается (удалённый туннель).
func TestPolicy_Prune_KeepsIncidentsOfSilentRouter(t *testing.T) {
	d := newTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	silent, err := d.Users().Insert("silent-owl", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "198.51.100.10", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	live, err := d.Users().Insert("live-lynx", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "198.51.100.11", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	ins := func(uid int64, check string, ts time.Time) {
		t.Helper()
		if err := d.Events().Insert(uid, check, "fail", "{}", ts); err != nil {
			t.Fatal(err)
		}
	}
	// Молчит 10 дней: последние события старше порога в 7 дней.
	ins(silent, "agent_heartbeat", now.Add(-10*24*time.Hour))
	ins(silent, "dns", now.Add(-10*24*time.Hour))
	// Живой: шлёт heartbeat и dns, а tunnel_awg12 удалён 9 дней назад.
	ins(live, "agent_heartbeat", now.Add(-time.Minute))
	ins(live, "dns", now.Add(-time.Minute))
	ins(live, "tunnel_awg12", now.Add(-9*24*time.Hour))

	hard := now.Add(-11 * 24 * time.Hour)
	save := func(uid int64, check string) {
		t.Helper()
		if err := d.State().Save(uid, check, db.IncidentState{UserID: uid, CheckName: check, CurrentStatus: "hard", ConsecutiveFails: 5, HardSince: &hard}); err != nil {
			t.Fatal(err)
		}
	}
	save(silent, "agent_heartbeat")
	save(silent, "dns")
	save(live, "dns")
	save(live, "tunnel_awg12")

	p := &Policy{DB: d, Cfg: Config{EventsDays: 30}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	if err := p.prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	has := func(uid int64, check string) bool {
		var n int
		if err := d.SQL().QueryRow(`SELECT count(*) FROM incident_state WHERE user_id = ? AND check_name = ?`, uid, check).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !has(silent, "agent_heartbeat") || !has(silent, "dns") {
		t.Error("инциденты молчащего роутера удалены")
	}
	if !has(live, "dns") {
		t.Error("инцидент живой проверки удалён")
	}
	if has(live, "tunnel_awg12") {
		t.Error("инцидент удалённого туннеля у отчитывающегося роутера не удалён")
	}
}
