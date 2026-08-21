package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// seed набивает базу парком, на котором видно все состояния мини-аппа: живой
// роутер, роутер с горящим инцидентом, мобильный в спячке и роутер со старым
// агентом. Без этого экраны открываются пустыми, и половину вёрстки нечем
// проверить -- а пустой экран как раз и не показывает ошибок разметки.
func seed(d *db.DB, tgUserID int64) error {
	now := time.Now().UTC()

	type routerSpec struct {
		nick     string
		kind     string
		version  string
		lastSeen time.Duration // как давно приходил отчёт
		role     string        // owner | operator
	}
	specs := []routerSpec{
		{"sandbox-home", "static", "v0.18.5", 40 * time.Second, "owner"},
		{"sandbox-broken", "static", "v0.18.5", 3 * time.Minute, "owner"},
		{"sandbox-car", "mobile", "v0.17.2", 4 * time.Hour, "operator"},
		{"sandbox-legacy", "static", "v0.14.4", 90 * time.Second, "operator"},
	}

	for i, s := range specs {
		token := strings.Repeat(fmt.Sprintf("%d", i+1), 64)
		uid, err := d.Users().InsertWithKind(s.nick, token, "203.0.113."+fmt.Sprint(10+i), "nwg1", s.kind)
		if err != nil {
			return fmt.Errorf("%s: %w", s.nick, err)
		}
		if err := d.Users().UpdateTelegramTopic(uid, -100500, int64(100+i)); err != nil {
			return err
		}
		if err := d.Users().UpdateLastSeenAgentVersion(uid, s.version); err != nil {
			return err
		}
		// Владелец роутера привязывается через users.telegram_user_id,
		// оператор -- отдельной строкой в router_operators. В песочнице нужны
		// оба пути: у них разные права, и экран настроек показывает разное.
		if s.role == "owner" {
			if err := d.Users().SetTelegramUserID(uid, tgUserID); err != nil {
				return err
			}
		} else if err := d.RouterOperators().Add(uid, tgUserID, tgUserID); err != nil {
			return err
		}

		seen := now.Add(-s.lastSeen)
		if err := seedChecks(d, uid, seen, s.nick == "sandbox-broken"); err != nil {
			return err
		}
		if err := d.Users().UpdateLastSeen(uid); err != nil {
			return err
		}
		if s.nick == "sandbox-broken" {
			hardSince := now.Add(-2 * time.Hour)
			lastAlert := now.Add(-30 * time.Minute)
			if err := d.State().Save(uid, "tunnel_awg12", db.IncidentState{
				UserID: uid, CheckName: "tunnel_awg12", CurrentStatus: "hard",
				ConsecutiveFails: 4, HardSince: &hardSince, LastAlertAt: &lastAlert,
			}); err != nil {
				return err
			}
		}
		if err := d.TunnelOrigins().Record(uid, "awg12", "awg12",
			"amnezia_premium", "Нидерланды", now.Add(-72*time.Hour), tgUserID); err != nil {
			return err
		}
	}
	return nil
}

// seedChecks кладёт по одному событию на проверку -- ровно те имена и ключи
// details, которые мини-апп умеет разбирать (см. miniapp_check_facts.go).
// Выдумывать ключи бессмысленно: проекция их отбросит, и экран покажет
// «нет данных» вместо фактов.
func seedChecks(d *db.DB, uid int64, ts time.Time, broken bool) error {
	tunnelOK := `{"iface":"awg12","handshake_age_sec":21,"rx_bytes":5741000000,"tx_bytes":3120000000,"peer":"nl-1.example","exit_ip":"203.0.113.77"}`
	tunnelBad := `{"iface":"awg12","handshake_age_sec":5400,"rx_bytes":0,"tx_bytes":8100,"peer":"nl-1.example","error":"no handshake for 90m"}`
	rows := []struct {
		name    string
		status  string
		details string
	}{
		{"agent_heartbeat", "ok", `{}`},
		{"dns", "ok", `{"endpoints":4,"failed_count":0,"rkn_probed":true,"rkn_suspect":false}`},
		{"hydraroute", "ok", `{"routes_hrneo":37,"routes_ndms":4,"routes_static":12,"active_backend":"hr_neo","singbox_router_active":true}`},
		{"awg_manager", "ok", `{"version":"2.17.2","firmware":"4.3.9"}`},
		{"external_reach", "ok", `{"targets_total":3,"targets_failed":[],"targets_degraded":[]}`},
		{"tunnel_awg12", "ok", tunnelOK},
		{"tunnel_awg10", "ok", `{"iface":"awg10","handshake_age_sec":48,"rx_bytes":880000000,"tx_bytes":140000000,"peer":"de-2.example"}`},
	}
	if broken {
		rows[5].status = "fail"
		rows[5].details = tunnelBad
		rows[1].status = "fail"
		rows[1].details = `{"endpoints":4,"failed_count":2,"rkn_probed":true,"rkn_suspect":true}`
	}
	// Несколько срезов во времени, иначе лента событий и график состоят из
	// одной точки, а именно на ленте ломается вёрстка длинных списков.
	for shift := 0; shift < 6; shift++ {
		at := ts.Add(-time.Duration(shift) * 7 * time.Minute)
		for _, r := range rows {
			status := r.status
			if shift > 0 && status == "fail" && shift%2 == 0 {
				status = "ok"
			}
			if err := d.Events().Insert(uid, r.name, status, r.details, at); err != nil {
				return err
			}
		}
	}
	return nil
}
