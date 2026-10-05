package backend

import (
	"encoding/json"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Тревога «трафик мимо VPN-туннеля» (v0.56, спека A).
//
// Правила роутера уводят сайты в VPN-туннель, VPN-туннель работает, а адрес в
// интернете с ним тот же, что и без него: заблокированное, скорее всего, не
// откроется. Агент этого не проверяет -- вердикт выводит бэкенд при приёме
// отчёта из того, что уже приходит: проверки tunnel_* (run_state, run_grace,
// routes_*), проверка hydraroute (policies, sing-box, ошибка чтения правил) и
// факт выхода (exit) по VPN-туннелю.
//
// Вердикт пишется строкой проверки bypass_leak в events тем же путём, что и
// строки агента: одна строка на отчёт. Порог хранится в самой строке
// (details.state) -- следующему отчёту нужна одна точечная выборка последней
// строки по индексу (user_id, check_name, ts), а не проход по истории и не
// память процесса, которая умирала бы с каждым рестартом бэкенда.
//
// Пока alerts.bypass_leak.enabled=false (по умолчанию), автомат тревог эту
// строку не ведёт, а экраны её не показывают: неделю копим вердикты и
// считаем, сколько тревог было бы (bypassLeakWeeklySQL).
const bypassLeakCheck = alerts.BypassLeakCheck

// Что показал отчёт про несущий VPN-туннель.
const (
	bypassLeakMismatch   = "mismatch"   // адрес с VPN тот же, что без него
	bypassLeakMatch      = "match"      // адрес с VPN другой -- всё как надо
	bypassLeakUnverified = "unverified" // судить не по чему (исключения спеки)
)

// Почему вердикт «не проверено». Каждое -- исключение спеки A: тревога в этих
// случаях была бы ложной или не на чем основанной.
const (
	bypassLeakReasonSingbox         = "singbox"          // sing-box выбирает маршрут сам
	bypassLeakReasonRulesUnreadable = "rules_unreadable" // правила не прочитаны
	bypassLeakReasonNoRules         = "no_rules_via_vpn" // правил в VPN-туннель нет
	bypassLeakReasonReserveOnly     = "reserve_only"     // правила только у запасного звена
	bypassLeakReasonRestartGrace    = "restart_grace"    // окно терпимости перезапуска
	bypassLeakReasonExitUnknown     = "exit_unknown"     // замера нет или он без ответа
	bypassLeakReasonExitStale       = "exit_stale"       // замер старше часа
	bypassLeakReasonNoDirectIP      = "no_direct_ip"     // нет адреса без VPN
	bypassLeakReasonEndpointInISP   = "endpoint_in_isp"  // VPN-сервер в сети провайдера
)

const (
	// bypassLeakExitFresh -- замер выхода старше часа ничего не говорит о
	// сейчас: агент меряет VPN-туннель раз в 20 минут.
	bypassLeakExitFresh = 60 * time.Minute
	// bypassLeakFailReports / bypassLeakFailProbes -- тревога после трёх
	// отчётов подряд с несовпадением, увиденным хотя бы на двух разных
	// замерах: один замер повторяется в каждом отчёте, пока не придёт новый.
	bypassLeakFailReports = 3
	bypassLeakFailProbes  = 2
	// bypassLeakRecoverProbes -- тревога снимается двумя разными замерами, на
	// которых адрес снова меняется.
	bypassLeakRecoverProbes = 2
	// bypassLeakStateMaxAge -- счёт из строки старше этого не продолжается:
	// роутер молчал, и «подряд» уже не про него.
	bypassLeakStateMaxAge = 2 * time.Hour
)

// bypassLeakHidden -- строку не показывать: тихий режим (экраны и лента
// роутера её не видят, пока тревога выключена).
func bypassLeakHidden(d Deps, checkName string) bool {
	return checkName == bypassLeakCheck && !d.AlertPolicy.BypassLeakEnabled
}

// bypassLeakObs -- что показал один отчёт.
type bypassLeakObs struct {
	Kind       string
	Reason     string
	TunnelID   string
	TunnelName string
	ProbeAt    time.Time
}

// bypassLeakState -- счёт порога, едет в details.state каждой строки.
type bypassLeakState struct {
	Alarm           bool   `json:"alarm"`
	TunnelID        string `json:"tunnel_id,omitempty"`
	MismatchReports int    `json:"mismatch_reports,omitempty"`
	MismatchProbes  int    `json:"mismatch_probes,omitempty"`
	LastMismatch    string `json:"last_mismatch_probe,omitempty"`
	MatchProbes     int    `json:"match_probes,omitempty"`
	LastMatch       string `json:"last_match_probe,omitempty"`
}

// bypassLeakObserve -- вердикт одного отчёта. exit зовётся только когда есть
// что сверять: факт выхода лежит в отдельной таблице, и роутеру без правил в
// VPN-туннель лишняя выборка не нужна.
func bypassLeakObserve(checks []wire.Check, exit func() *wire.ExitFacts, now time.Time) bypassLeakObs {
	unverified := func(reason string) bypassLeakObs {
		return bypassLeakObs{Kind: bypassLeakUnverified, Reason: reason}
	}
	var (
		hd      miniappHydraDetails
		hdSeen  bool
		tunnels []miniappTunnel
		// policiesErr -- агент не прочитал сводку политик (policies_error) и
		// оставил её пустой. Без неё не видно, кто запасное звено, а правила
		// без явного маршрута агент приписывает первому VPN-туннелю с главным
		// маршрутом -- резерв судился бы как несущий.
		policiesErr string
	)
	for _, c := range checks {
		row := db.EventRow{CheckName: c.Name, Status: c.Status, DetailsJSON: normaliseDetailsJSON(c.Details)}
		if c.Name == "hydraroute" {
			hdSeen = json.Unmarshal([]byte(row.DetailsJSON), &hd) == nil
			var pe struct {
				PoliciesError string `json:"policies_error"`
			}
			if hdSeen && json.Unmarshal([]byte(row.DetailsJSON), &pe) == nil {
				policiesErr = pe.PoliciesError
			}
			continue
		}
		if t, ok := miniappTunnelFromEvent(row); ok {
			tunnels = append(tunnels, t)
		}
	}
	switch {
	case hdSeen && hd.SingboxRouterActive:
		return unverified(bypassLeakReasonSingbox)
	case !hdSeen || hd.MechanismProbeError != "" || policiesErr != "":
		return unverified(bypassLeakReasonRulesUnreadable)
	}

	candidates, reserveOnly := bypassLeakCarriers(tunnels, hd)
	if len(candidates) == 0 {
		if reserveOnly {
			return unverified(bypassLeakReasonReserveOnly)
		}
		return unverified(bypassLeakReasonNoRules)
	}

	facts := exit()
	var first, match *bypassLeakObs
	for _, t := range candidates {
		o := bypassLeakJudge(t, facts, now)
		switch o.Kind {
		case bypassLeakMismatch:
			return o
		case bypassLeakMatch:
			if match == nil {
				match = &o
			}
		}
		if first == nil {
			first = &o
		}
	}
	if match != nil {
		return *match
	}
	return *first
}

// bypassLeakCarriers -- VPN-туннели, через которые роутер исполняет правила:
// активное звено политики с исполняемыми правилами (правила HydraRoute Neo --
// только при запущенном HydraRoute), либо VPN-туннель со своими правилами
// (routes_dns/routes_static), если он не только запасное звено набора.
// Остановленный на разовый перезапуск (run_grace) -- тоже несущий: его
// вердикт будет «не проверено».
//
// reserveOnly -- правила нашлись только у запасных звеньев.
func bypassLeakCarriers(tunnels []miniappTunnel, hd miniappHydraDetails) (out []*miniappTunnel, reserveOnly bool) {
	seen := map[string]bool{}
	add := func(t *miniappTunnel) {
		if t != nil && !seen[t.TunnelID] && miniappTunnelCarriesRules(t) {
			seen[t.TunnelID] = true
			out = append(out, t)
		}
	}
	activeOf := map[string]bool{}
	fallbackOf := map[string]bool{}
	for i := range hd.Policies {
		p := &hd.Policies[i]
		if p.ActiveTunnelID != "" {
			activeOf[p.ActiveTunnelID] = true
		}
		for _, l := range p.Links {
			if l.TunnelID != "" && l.TunnelID != p.ActiveTunnelID {
				fallbackOf[l.TunnelID] = true
			}
		}
		if miniappPolicyExecuted(p, hd) <= 0 || !p.ViaVPN || p.ActiveTunnelID == "" {
			continue
		}
		add(miniappTunnelByID(tunnels, p.ActiveTunnelID))
	}
	for i := range tunnels {
		t := &tunnels[i]
		if t.RoutesDNS+t.RoutesStatic <= 0 {
			continue
		}
		if fallbackOf[t.TunnelID] && !activeOf[t.TunnelID] {
			// Правила без явного маршрута агент приписывает первому
			// заявившему основной маршрут -- запасное звено их не везёт.
			if miniappTunnelCarriesRules(t) {
				reserveOnly = true
			}
			continue
		}
		add(t)
	}
	if len(out) > 0 {
		reserveOnly = false
	}
	return out, reserveOnly
}

// bypassLeakJudge -- сверка одного несущего VPN-туннеля с его замером выхода.
func bypassLeakJudge(t *miniappTunnel, facts *wire.ExitFacts, now time.Time) bypassLeakObs {
	o := bypassLeakObs{Kind: bypassLeakUnverified, TunnelID: t.TunnelID, TunnelName: t.Name}
	if t.RunState != "running" {
		o.Reason = bypassLeakReasonRestartGrace
		return o
	}
	var p wire.ExitProbe
	ok := false
	if facts != nil {
		p, ok = facts.Tunnels[t.TunnelID]
	}
	switch {
	case !ok:
		o.Reason = bypassLeakReasonExitUnknown
		return o
	case p.At.IsZero() || now.Sub(p.At) > bypassLeakExitFresh:
		o.Reason = bypassLeakReasonExitStale
		return o
	case p.Changed == nil:
		o.Reason = bypassLeakReasonExitUnknown
		return o
	case p.DirectIP == "":
		o.Reason = bypassLeakReasonNoDirectIP
		return o
	case p.EndpointIP != "" && p.EndpointIP == p.DirectIP:
		// VPN-сервер в сети того же провайдера -- так настроил человек;
		// тревога была бы ложной.
		o.Reason = bypassLeakReasonEndpointInISP
		return o
	}
	o.ProbeAt = p.At.UTC()
	if *p.Changed {
		o.Kind = bypassLeakMatch
		return o
	}
	if p.VPNIP != "" && p.VPNIP == p.DirectIP {
		o.Kind = bypassLeakMismatch
		return o
	}
	o.Reason = bypassLeakReasonExitUnknown
	o.ProbeAt = time.Time{}
	return o
}

// bypassLeakStep -- счёт порога после очередного отчёта.
//
// Непроверенный отчёт рвёт набирающуюся серию («подряд» -- это подряд), но
// поднятую тревогу не снимает: снять её вправе только замеры с изменившимся
// адресом. Сменился несущий VPN-туннель -- счёт заново: трафик пошёл другим
// путём, и прошлые несовпадения не про него.
func bypassLeakStep(prev bypassLeakState, o bypassLeakObs) bypassLeakState {
	if o.Kind == bypassLeakUnverified {
		if !prev.Alarm {
			return bypassLeakState{TunnelID: prev.TunnelID}
		}
		return prev
	}
	next := prev
	if next.TunnelID != o.TunnelID {
		next = bypassLeakState{TunnelID: o.TunnelID}
	}
	probe := o.ProbeAt.UTC().Format(time.RFC3339)
	switch o.Kind {
	case bypassLeakMismatch:
		next.MatchProbes, next.LastMatch = 0, ""
		next.MismatchReports++
		if probe != next.LastMismatch {
			next.MismatchProbes++
			next.LastMismatch = probe
		}
		if next.MismatchReports >= bypassLeakFailReports && next.MismatchProbes >= bypassLeakFailProbes {
			next.Alarm = true
		}
	case bypassLeakMatch:
		next.MismatchReports, next.MismatchProbes, next.LastMismatch = 0, 0, ""
		if !next.Alarm {
			next.MatchProbes, next.LastMatch = 0, ""
			break
		}
		if probe != next.LastMatch {
			next.MatchProbes++
			next.LastMatch = probe
		}
		if next.MatchProbes >= bypassLeakRecoverProbes {
			next.Alarm = false
			next.MatchProbes, next.LastMatch = 0, ""
		}
	}
	return next
}

// bypassLeakResume -- счёт из прошлой строки после паузы gap. Долгая пауза
// (роутер молчал, мобильный спал) рвёт только набирающуюся серию: «подряд»
// уже не про него. Поднятую тревогу пауза не снимает -- иначе после сна шла
// бы строка ok, автомат засчитал бы «снова меняет адрес», а следующие
// несовпадения дали бы новую тревогу. Снять её вправе только замеры
// (bypassLeakRecoverProbes).
func bypassLeakResume(prev bypassLeakState, gap time.Duration) bypassLeakState {
	if gap <= bypassLeakStateMaxAge || prev.Alarm {
		return prev
	}
	return bypassLeakState{}
}

// bypassLeakApply -- переход автомата тревог для bypass_leak. Порог «три
// отчёта на двух замерах» и снятие «два замера» бэкенд уже отсчитал в самой
// строке: первая строка fail и есть тревога, первая ok после неё -- снятие.
// Общий автомат добавил бы свою задержку (ok→fail у него Soft, HARD -- только
// на следующем fail; снятие -- после Recovery ok подряд).
func bypassLeakApply(prev db.IncidentState, incoming string, now time.Time) state.Transition {
	th := state.Thresholds{Fail: 1, Recovery: 1}
	if incoming == "fail" && prev.CurrentStatus != "hard" {
		next := prev
		next.ConsecutiveFails = prev.ConsecutiveFails + 1
		next.ConsecutiveOKs = 0
		next.CurrentStatus = "hard"
		t := now
		next.HardSince = &t
		next.LastAlertAt = &t
		return state.Transition{Kind: state.Hard, Next: next}
	}
	return state.Apply(prev, incoming, now, th)
}

// bypassLeakStatus -- статус строки: fail только у поднятой тревоги на
// проверенном отчёте. Непроверенный -- ok с unverified=true: автомат тревог
// им не двигается ни в какую сторону (checkUnverified).
func bypassLeakStatus(st bypassLeakState, o bypassLeakObs) string {
	if st.Alarm && o.Kind != bypassLeakUnverified {
		return "fail"
	}
	return "ok"
}

// bypassLeakCheckOf -- строка проверки. external_reach -- статус доступности
// сервисов того же отчёта: от него цвет тревоги (жёлтый, пока сервисы
// открываются).
func bypassLeakCheckOf(st bypassLeakState, o bypassLeakObs, externalReach string) wire.Check {
	d := map[string]any{"state": st}
	if o.TunnelID != "" {
		d["tunnel_id"] = o.TunnelID
	}
	if o.TunnelName != "" {
		d["tunnel_name"] = o.TunnelName
	}
	if externalReach != "" {
		d["external_reach"] = externalReach
	}
	if o.Kind == bypassLeakUnverified {
		d["unverified"] = true
		d["reason"] = o.Reason
	} else {
		d["observed"] = o.Kind
		d["exit_at"] = o.ProbeAt.UTC().Format(time.RFC3339)
	}
	return wire.Check{Name: bypassLeakCheck, Status: bypassLeakStatus(st, o), Details: d}
}

// bypassLeakReportCheck -- строка bypass_leak для принимаемого отчёта.
// Факт выхода -- из самого отчёта, иначе последний сохранённый: агент шлёт
// блок только когда он изменился или давно не подтверждался. Счёт порога --
// из последней строки bypass_leak этого роутера.
func bypassLeakReportCheck(d Deps, uid int64, rep *wire.Report, ts time.Time) wire.Check {
	exit := func() *wire.ExitFacts {
		if rep.Facts != nil && rep.Facts.Exit != nil {
			return rep.Facts.Exit
		}
		all, err := d.DB.RouterFacts().All(uid)
		if err != nil {
			d.Logger.Warn("bypass_leak: факт выхода не прочитан", "router_id", uid, "err", err)
			return nil
		}
		f, ok := all[db.FactExit]
		if !ok {
			return nil
		}
		var out wire.ExitFacts
		if json.Unmarshal(f.Body, &out) != nil {
			return nil
		}
		return &out
	}
	o := bypassLeakObserve(rep.Checks, exit, ts)

	var prev bypassLeakState
	if row, ok, err := d.DB.Events().LatestEvent(uid, bypassLeakCheck); err != nil {
		d.Logger.Warn("bypass_leak: прошлая строка не прочитана", "router_id", uid, "err", err)
	} else if ok && row.TS.Before(ts) {
		var pd struct {
			State bypassLeakState `json:"state"`
		}
		if json.Unmarshal([]byte(row.DetailsJSON), &pd) == nil {
			prev = bypassLeakResume(pd.State, ts.Sub(row.TS))
		}
	}
	reach := ""
	for _, c := range rep.Checks {
		if c.Name == "external_reach" {
			reach = c.Status
			break
		}
	}
	return bypassLeakCheckOf(bypassLeakStep(prev, o), o, reach)
}

// bypassLeakWeeklySQL -- сколько тревог «мимо VPN-туннеля» было бы за период
// (параметры -- начало периода четырежды). episodes -- переходы в тревогу:
// строка с поднятой тревогой (state.alarm), у которой предыдущая строка
// роутера без неё. Строки «не проверено» при поднятой тревоге несут alarm и
// эпизод не дробят. Тревога, поднятая до начала периода, в нём эпизодом не
// считается. Все подзапросы идут по индексу (user_id, check_name, ts):
// полный проход по горячей events недопустим. Копия для ручного прогона --
// docs/operations (вне git).
const bypassLeakWeeklySQL = `
SELECT u.nickname,
  (SELECT COUNT(*) FROM events e
    WHERE e.user_id = u.id AND e.check_name = 'bypass_leak' AND e.ts >= ?) AS reports,
  (SELECT COUNT(*) FROM events e
    WHERE e.user_id = u.id AND e.check_name = 'bypass_leak' AND e.ts >= ?
      AND json_extract(e.details_json, '$.state.alarm') = 1
      AND COALESCE((SELECT json_extract(p.details_json, '$.state.alarm') FROM events p
                     WHERE p.user_id = e.user_id AND p.check_name = 'bypass_leak' AND p.ts < e.ts
                     ORDER BY p.ts DESC LIMIT 1), 0) = 0) AS episodes,
  (SELECT COUNT(*) FROM events e
    WHERE e.user_id = u.id AND e.check_name = 'bypass_leak' AND e.ts >= ? AND e.status = 'fail') AS fails,
  (SELECT COUNT(*) FROM events e
    WHERE e.user_id = u.id AND e.check_name = 'bypass_leak' AND e.ts >= ?
      AND e.details_json LIKE '%"unverified":true%') AS unverified
FROM users u
ORDER BY episodes DESC, fails DESC, u.nickname`
