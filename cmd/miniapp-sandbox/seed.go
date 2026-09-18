package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// seed набивает базу парком, на котором видно все состояния мини-аппа: живой
// роутер, роутер с горящим инцидентом, мобильный в спячке, роутер со старым
// агентом и выключенный роутер с отложенным обновлением агента. Без этого
// экраны открываются пустыми, и половину вёрстки нечем проверить -- а пустой
// экран как раз и не показывает ошибок разметки.
func seed(d *db.DB, tgUserID int64) (map[string]int64, error) {
	now := time.Now().UTC()
	ids := map[string]int64{}

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
		// Выключенный роутер с отложенным обновлением агента: так на экране
		// «Парк» видно «ждёт включения» -- ровно случай оператора 15.09
		// («три необновлённых роутера выключены»).
		{"sandbox-off", "static", "v0.30.0", 96 * time.Hour, "owner"},
		// Выключенный роутер без адреса панели -- случай bronya из парка
		// 15.09: оживлению нужен адрес, и лист обязан его спросить.
		{"sandbox-bronya", "static", "v0.29.0", 240 * time.Hour, "owner"},
		// Форма workrouter 18.09: обход несёт живой awg14, запасное звено awg10
		// мертво. Экран обязан сказать «всё работает, резерва нет», а не
		// красить ветку по мёртвому запасному.
		{"sandbox-work", "static", "v0.41.0", 30 * time.Second, "owner"},
	}

	for i, s := range specs {
		token := strings.Repeat(fmt.Sprintf("%d", i+1), 64)
		uid, err := d.Users().InsertWithKind(s.nick, token, "203.0.113."+fmt.Sprint(10+i), "nwg1", s.kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.nick, err)
		}
		ids[s.nick] = uid
		if err := d.Users().UpdateTelegramTopic(uid, -100500, int64(100+i)); err != nil {
			return nil, err
		}
		if err := d.Users().UpdateLastSeenAgentVersion(uid, s.version); err != nil {
			return nil, err
		}
		// Владелец роутера привязывается через users.telegram_user_id,
		// оператор -- отдельной строкой в router_operators. В песочнице нужны
		// оба пути: у них разные права, и экран настроек показывает разное.
		if s.role == "owner" {
			if err := d.Users().SetTelegramUserID(uid, tgUserID); err != nil {
				return nil, err
			}
		} else if err := d.RouterOperators().Add(uid, tgUserID, tgUserID); err != nil {
			return nil, err
		}

		seen := now.Add(-s.lastSeen)
		if s.nick == "sandbox-work" {
			if err := seedWorkChecks(d, uid, seen); err != nil {
				return nil, err
			}
		} else if err := seedChecks(d, uid, seen, s.nick == "sandbox-broken"); err != nil {
			return nil, err
		}
		// История за неделю: без неё вкладка «Что было» открывается почти
		// пустой, и ни свёрнутое моргание, ни тихий день проверить нечем --
		// а это ровно то, ради чего лента переписана.
		if err := seedHistory(d, uid, now, s.nick); err != nil {
			return nil, err
		}
		if err := d.Users().UpdateLastSeen(uid); err != nil {
			return nil, err
		}
		if s.nick == "sandbox-off" || s.nick == "sandbox-bronya" {
			// UpdateLastSeen выше ставит «сейчас»; выключенному нужен старый
			// отчёт, иначе сводка посчитает его живым.
			if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, seen.UTC().Format(time.RFC3339), uid); err != nil {
				return nil, err
			}
		}
		if s.nick != "sandbox-off" && s.nick != "sandbox-bronya" {
			// Адрес панели у живых роутеров: переустановка и перенаправление
			// агента (цикл 2) идут через терминал панели и без адреса честно
			// отказывают no_awgm_url -- экран «Ход работы» было бы нечем
			// проверить. У sandbox-bronya адреса нет намеренно (см. выше).
			if _, err := d.SQL().Exec(`UPDATE users SET awgm_url = ?, awgm_auth = ? WHERE id = ?`,
				fmt.Sprintf("https://203.0.113.%d:2222", 10+i), "web", uid); err != nil {
				return nil, err
			}
		}
		if s.nick == "sandbox-off" {
			if err := d.Users().MarkPendingDeploy(uid, "v0.33.0", now.Add(-72*time.Hour).Format(time.RFC3339)); err != nil {
				return nil, err
			}
			// Адрес панели записан: в приложение уходит только признак.
			if _, err := d.SQL().Exec(`UPDATE users SET awgm_url = ? WHERE id = ?`, "https://203.0.113.14:2222", uid); err != nil {
				return nil, err
			}
		}
		if s.nick == "sandbox-work" {
			// Адрес панели -- доменом, как у настоящих роутеров: мини-апп
			// показывает хост строкой под именем.
			if _, err := d.SQL().Exec(`UPDATE users SET awgm_url = ? WHERE id = ?`, "https://awg.example.com", uid); err != nil {
				return nil, err
			}
			hardSince := now.Add(-20 * time.Minute)
			lastAlert := now.Add(-19 * time.Minute)
			if err := d.State().Save(uid, "tunnel_awg10", db.IncidentState{
				UserID: uid, CheckName: "tunnel_awg10", CurrentStatus: "hard",
				ConsecutiveFails: 22, HardSince: &hardSince, LastAlertAt: &lastAlert,
			}); err != nil {
				return nil, err
			}
		}
		if s.nick == "sandbox-broken" {
			hardSince := now.Add(-2 * time.Hour)
			lastAlert := now.Add(-30 * time.Minute)
			if err := d.State().Save(uid, "tunnel_awg12", db.IncidentState{
				UserID: uid, CheckName: "tunnel_awg12", CurrentStatus: "hard",
				ConsecutiveFails: 4, HardSince: &hardSince, LastAlertAt: &lastAlert,
			}); err != nil {
				return nil, err
			}
		}
		// Провайдер и вариант -- ровно те, какими их пишет мастер замены
		// (идентификатор опции, а не её подпись): починка перевыпускает по
		// ним же, и разойтись они не имеют права.
		if err := d.TunnelOrigins().Record(uid, "awg12", "vpn-nl",
			"amnezia", "nl", now.Add(-72*time.Hour), tgUserID); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// seedChecks кладёт по одному событию на проверку -- ровно те имена и ключи
// details, которые мини-апп умеет разбирать (см. miniapp_check_facts.go).
// Выдумывать ключи бессмысленно: проекция их отбросит, и экран покажет
// «нет данных» вместо фактов.
func seedChecks(d *db.DB, uid int64, ts time.Time, broken bool) error {
	// Ключи -- те, что разбирает miniappTunnelDetails (miniapp_tunnels.go).
	// Выдумывать свои бессмысленно: проекция их отбросит, и экран честно
	// скажет «0 линий поднято» о поднятых туннелях -- то есть песочница
	// станет учить неправде.
	tunnelOK := `{"tunnel_id":"awg12","tunnel_name":"vpn-nl","status":"running","enabled":true,"handshake_age_sec":21,"ping_check_status":"ok","ping_check_last_latency_ms":38,"matrix_latency_ms":84,"matrix_updated_at":"2026-09-09T09:32:23Z","default_route_intent":true,"is_active_default":true,"active_default_known":true}`
	tunnelBad := `{"tunnel_id":"awg12","tunnel_name":"vpn-nl","status":"down","enabled":true,"handshake_age_sec":5400,"ping_check_status":"fail","default_route_intent":true,"is_active_default":false,"active_default_known":true,"note":"рукопожатия нет 90 минут"}`
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
		// Сторож своего DNS-сервера: проверка вне основного порядка экрана,
		// и только на ней видно, что её имя переведено («Свой DNS-сервер»).
		{"resolver_guard", "ok", `{}`},
		{"tunnel_awg10", "ok", `{"tunnel_id":"awg10","tunnel_name":"vpn-de","status":"running","enabled":true,"handshake_age_sec":48,"ping_check_status":"ok","ping_check_last_latency_ms":52,"matrix_latency_ms":226,"matrix_updated_at":"2026-09-09T09:32:23Z","active_default_known":true}`},
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

// seedWorkChecks -- снимок прода workrouter 18.09 07:58 (обезличен): два
// поднятых VPN-туннеля, оба заявляют основной маршрут, правила HydraRoute
// ведут в политику, где awg14 -- активное звено, awg10 -- запасное. awg10
// не отвечает (обмен ключами 24 минуты назад). details.policies -- форма
// v0.41 (wire.PolicyBrief): по ней бэкенд называет несущего; бэкенд старше
// её не читает, и экран обязан не угадывать.
func seedWorkChecks(d *db.DB, uid int64, ts time.Time) error {
	rows := []struct {
		name    string
		status  string
		details string
	}{
		{"agent_heartbeat", "ok", `{}`},
		{"dns", "ok", `{"endpoints":0,"failed_count":0}`},
		{"hydraroute", "ok", `{"running":true,"routes_hrneo":32,"routes_ndms":0,"routes_static":0,"active_backend":"kernel",` +
			`"policies":[{"name":"HydraRoute","active_tunnel_id":"awg14","via_vpn":true,"dns":32,"hr_neo":32,` +
			`"links":[{"tunnel_id":"awg14","role":"active"},{"tunnel_id":"awg10","role":"fallback"}]}]}`},
		{"awg_manager", "ok", `{"version":"2.19.1","firmware":"5.02.A.8.0-3"}`},
		{"tunnel_awg10", "fail", `{"tunnel_id":"awg10","tunnel_name":"vpn-nl","status":"running","enabled":true,"handshake_age_sec":1447,"ping_check_status":"disabled","default_route_intent":true,"is_active_default":false,"active_default_known":true}`},
		{"tunnel_awg14", "ok", `{"tunnel_id":"awg14","tunnel_name":"vpn-hip","status":"running","enabled":true,"handshake_age_sec":88,"ping_check_status":"disabled","matrix_latency_ms":117,"matrix_updated_at":"2026-09-18T07:58:02Z","default_route_intent":true,"is_active_default":false,"active_default_known":true,"note":"обмен ключами устарел от простоя, но VPN-туннель отвечает на пробу"}`},
	}
	for shift := 0; shift < 3; shift++ {
		at := ts.Add(-time.Duration(shift) * time.Minute)
		for _, r := range rows {
			if err := d.Events().Insert(uid, r.name, r.status, r.details, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// seedHistory набивает недельную историю происшествий: у здорового роутера
// один короткий отвал, у сломанного -- моргание и идущая поломка, у
// мобильного -- ничего, чтобы было видно тихие дни.
//
// Пишем ПАРЫ событий, а не срезы состояния: сворачивание на бэкенде ищет
// именно пару «упало -- поднялось», и засев одиночными строками научил бы
// песочницу неправде.
func seedHistory(d *db.DB, uid int64, now time.Time, nick string) error {
	pair := func(check string, start time.Time, down time.Duration) error {
		if err := d.Events().Insert(uid, check, "fail", "{}", start); err != nil {
			return err
		}
		return d.Events().Insert(uid, check, "ok", "{}", start.Add(down))
	}

	switch nick {
	case "sandbox-home":
		// Вчера четыре минуты не было интернета -- одна строка в ленте.
		return pair("external_reach", now.Add(-26*time.Hour), 4*time.Minute)
	case "sandbox-broken":
		// Двенадцать морганий обхода блокировок внутри полутора часов: они
		// обязаны схлопнуться в ОДНУ строку с числом раз.
		for i := 0; i < 12; i++ {
			start := now.Add(-3*time.Hour + time.Duration(i)*7*time.Minute)
			if err := pair("hydraroute", start, time.Minute); err != nil {
				return err
			}
		}
		// Позавчерашний отвал панели -- отдельная новость, не слипается.
		if err := pair("awg_manager", now.Add(-50*time.Hour), 12*time.Minute); err != nil {
			return err
		}
		// Идущая поломка: конца у неё нет, и экран обязан сказать «идёт».
		return d.Events().Insert(uid, "external_reach", "fail", "{}", now.Add(-40*time.Minute))
	default:
		// Тихая неделя -- тоже состояние экрана, и его надо видеть.
		return nil
	}
}
