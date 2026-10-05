package backend

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// --- сырьё отчёта ---

func leakTunnel(id, name, run string, routesDNS int) wire.Check {
	return wire.Check{Name: "tunnel_" + id, Status: "ok", Details: map[string]any{
		"tunnel_id": id, "tunnel_name": name, "status": run, "routes_dns": routesDNS,
	}}
}

func leakTunnelGrace(id, name string, routesDNS int) wire.Check {
	c := leakTunnel(id, name, "stopped", routesDNS)
	c.Details["run_grace"] = true
	return c
}

func leakHydra(running bool, policies ...wire.PolicyBrief) wire.Check {
	d := map[string]any{"installed": true, "running": running}
	if len(policies) > 0 {
		var ps []any
		b, _ := json.Marshal(policies)
		_ = json.Unmarshal(b, &ps)
		d["policies"] = ps
	}
	return wire.Check{Name: "hydraroute", Status: "ok", Details: d}
}

func leakPolicy(active string, dns int, links ...wire.PolicyBriefLink) wire.PolicyBrief {
	return wire.PolicyBrief{Name: "Обход", ActiveTunnelID: active, ViaVPN: true, DNS: dns, Links: links}
}

func leakProbe(changed *bool, vpn, direct string, at time.Time) wire.ExitProbe {
	return wire.ExitProbe{VPNIP: vpn, DirectIP: direct, Changed: changed, Source: "agent", At: at}
}

func boolp(b bool) *bool { return &b }

func leakExit(at time.Time, probes map[string]wire.ExitProbe) *wire.ExitFacts {
	return &wire.ExitFacts{At: at, Tunnels: probes}
}

func observe(checks []wire.Check, exit *wire.ExitFacts, now time.Time) bypassLeakObs {
	return bypassLeakObserve(checks, func() *wire.ExitFacts { return exit }, now)
}

// --- условия и исключения спеки A ---

func TestBypassLeakObserve(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	same := leakProbe(boolp(false), "203.0.113.7", "203.0.113.7", now.Add(-5*time.Minute))
	moved := leakProbe(boolp(true), "198.51.100.9", "203.0.113.7", now.Add(-5*time.Minute))
	carrier := leakTunnel("awg12", "Франкфурт", "running", 0)
	policyHydra := leakHydra(true, leakPolicy("awg12", 40))

	cases := []struct {
		name       string
		checks     []wire.Check
		exit       *wire.ExitFacts
		wantKind   string
		wantReason string
		wantTunnel string
	}{
		{"политика ведёт в VPN-туннель, адрес не изменился", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakMismatch, "", "awg12"},
		{"правила VPN-туннеля без политик, адрес не изменился",
			[]wire.Check{leakTunnel("awg12", "Франкфурт", "running", 12), leakHydra(true)},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakMismatch, "", "awg12"},
		{"адрес изменился", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": moved}), bypassLeakMatch, "", "awg12"},
		{"sing-box", []wire.Check{carrier, {Name: "hydraroute", Status: "ok", Details: map[string]any{"singbox_router_active": true}}},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonSingbox, ""},
		{"правила не прочитаны", []wire.Check{carrier, {Name: "hydraroute", Status: "ok", Details: map[string]any{
			"running": true, "mechanism_probe_error": "too many"}}},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonRulesUnreadable, ""},
		{"проверки механизма нет в отчёте", []wire.Check{leakTunnel("awg12", "Франкфурт", "running", 12)},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonRulesUnreadable, ""},
		{"факт устарел", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": leakProbe(boolp(false), "203.0.113.7", "203.0.113.7", now.Add(-61*time.Minute))}),
			bypassLeakUnverified, bypassLeakReasonExitStale, "awg12"},
		{"замера нет", []wire.Check{carrier, policyHydra}, nil, bypassLeakUnverified, bypassLeakReasonExitUnknown, "awg12"},
		{"замер без ответа", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": leakProbe(nil, "", "203.0.113.7", now.Add(-time.Minute))}),
			bypassLeakUnverified, bypassLeakReasonExitUnknown, "awg12"},
		{"окно терпимости перезапуска", []wire.Check{leakTunnelGrace("awg12", "Франкфурт", 0), policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonRestartGrace, "awg12"},
		{"только резерв", []wire.Check{
			leakTunnel("awg10", "Амстердам", "stopped", 0),
			leakTunnel("awg12", "Франкфурт", "running", 30),
			leakHydra(true, leakPolicy("awg10", 40,
				wire.PolicyBriefLink{TunnelID: "awg10", Role: "active"},
				wire.PolicyBriefLink{TunnelID: "awg12", Role: "fallback"})),
		}, leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonReserveOnly, ""},
		{"пустой адрес без VPN", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": leakProbe(boolp(false), "203.0.113.7", "", now.Add(-time.Minute))}),
			bypassLeakUnverified, bypassLeakReasonNoDirectIP, "awg12"},
		{"VPN-сервер в сети провайдера", []wire.Check{carrier, policyHydra},
			leakExit(now, map[string]wire.ExitProbe{"awg12": {VPNIP: "203.0.113.7", DirectIP: "203.0.113.7", EndpointIP: "203.0.113.7",
				Changed: boolp(false), At: now.Add(-time.Minute)}}),
			bypassLeakUnverified, bypassLeakReasonEndpointInISP, "awg12"},
		{"правила HydraRoute Neo при остановленном HydraRoute не исполняются", []wire.Check{carrier,
			leakHydra(false, wire.PolicyBrief{Name: "Обход", ActiveTunnelID: "awg12", ViaVPN: true, DNS: 40, HRNeo: 40})},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonNoRules, ""},
		{"правил в VPN нет", []wire.Check{leakTunnel("awg12", "Франкфурт", "running", 0), leakHydra(true)},
			leakExit(now, map[string]wire.ExitProbe{"awg12": same}), bypassLeakUnverified, bypassLeakReasonNoRules, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := observe(tc.checks, tc.exit, now)
			if got.Kind != tc.wantKind || got.Reason != tc.wantReason || got.TunnelID != tc.wantTunnel {
				t.Fatalf("got %+v, want kind=%s reason=%q tunnel=%q", got, tc.wantKind, tc.wantReason, tc.wantTunnel)
			}
		})
	}
}

// --- порог: 3 подряд отчёта на двух разных замерах, сброс -- 2 замера ---

func leakObs(kind string, probe time.Time) bypassLeakObs {
	return bypassLeakObs{Kind: kind, TunnelID: "awg12", TunnelName: "Франкфурт", ProbeAt: probe}
}

func leakRun(t *testing.T, obs ...bypassLeakObs) (bypassLeakState, []string) {
	t.Helper()
	var st bypassLeakState
	var statuses []string
	for _, o := range obs {
		st = bypassLeakStep(st, o)
		statuses = append(statuses, bypassLeakStatus(st, o))
	}
	return st, statuses
}

func TestBypassLeakThreshold(t *testing.T) {
	p1 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	p2 := p1.Add(20 * time.Minute)
	p3 := p2.Add(20 * time.Minute)
	mm := func(p time.Time) bypassLeakObs { return leakObs(bypassLeakMismatch, p) }
	ok := func(p time.Time) bypassLeakObs { return leakObs(bypassLeakMatch, p) }
	unv := bypassLeakObs{Kind: bypassLeakUnverified, Reason: bypassLeakReasonExitStale, TunnelID: "awg12"}

	t.Run("три отчёта на одном замере -- не тревога", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p1), mm(p1), mm(p1))
		if strings.Contains(strings.Join(s, ","), "fail") {
			t.Fatalf("один замер дал fail: %v", s)
		}
	})
	t.Run("третий отчёт на втором замере -- тревога", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p1), mm(p2))
		if s[1] != "ok" || s[2] != "fail" {
			t.Fatalf("want ok,ok,fail, got %v", s)
		}
	})
	t.Run("два замера, но два отчёта -- рано", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p2))
		if s[1] != "ok" {
			t.Fatalf("want ok на втором отчёте, got %v", s)
		}
	})
	t.Run("непроверенный отчёт рвёт серию", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p1), unv, mm(p2), mm(p2))
		if s[4] != "ok" {
			t.Fatalf("серия не порвалась: %v", s)
		}
	})
	t.Run("совпадение рвёт серию", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p1), ok(p2), mm(p3), mm(p3))
		if s[4] != "ok" {
			t.Fatalf("серия не порвалась: %v", s)
		}
	})
	t.Run("сброс -- два замера с изменившимся адресом", func(t *testing.T) {
		_, s := leakRun(t, mm(p1), mm(p1), mm(p2), ok(p3), ok(p3), ok(p3), ok(p3.Add(20*time.Minute)))
		want := []string{"ok", "ok", "fail", "fail", "fail", "fail", "ok"}
		if strings.Join(s, ",") != strings.Join(want, ",") {
			t.Fatalf("got %v, want %v", s, want)
		}
	})
	t.Run("непроверенный отчёт тревогу не снимает и автомат не двигает", func(t *testing.T) {
		st, s := leakRun(t, mm(p1), mm(p1), mm(p2), unv)
		if !st.Alarm || s[3] != "ok" {
			t.Fatalf("alarm=%v statuses=%v", st.Alarm, s)
		}
		c := bypassLeakCheckOf(st, unv, "")
		if !checkUnverified(c) {
			t.Fatalf("строка без unverified: %#v", c.Details)
		}
		_, s = leakRun(t, mm(p1), mm(p1), mm(p2), unv, mm(p3))
		if s[4] != "fail" {
			t.Fatalf("тревога потерялась после непроверенного: %v", s)
		}
	})
	t.Run("сменился несущий VPN-туннель -- счёт заново", func(t *testing.T) {
		other := mm(p3)
		other.TunnelID = "awg10"
		_, s := leakRun(t, mm(p1), mm(p1), mm(p2), other)
		if s[3] != "ok" {
			t.Fatalf("тревога перешла на другой VPN-туннель: %v", s)
		}
	})
}

// --- приём отчёта ---

type leakHarness struct {
	t    *testing.T
	d    *db.DB
	disp *fakeDisp
	url  string
	tok  string
	base time.Time
	seq  int
}

func newLeakHarness(t *testing.T, enabled bool) *leakHarness {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	tok := "6161616161616161616161616161616161616161616161616161616161616161"
	if _, err := d.Users().Insert("leakrouter", tok, "198.51.100.11", "awg0"); err != nil {
		t.Fatal(err)
	}
	disp := &fakeDisp{db: d}
	srv := httptest.NewServer(NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
		AlertPolicy: AlertPolicy{NoisyFailThreshold: 6, NoisyRecoveryThreshold: 3,
			BypassLeakEnabled: enabled},
	}))
	t.Cleanup(srv.Close)
	return &leakHarness{t: t, d: d, disp: disp, url: srv.URL, tok: tok, base: time.Now().UTC().Add(-10 * time.Minute)}
}

func (h *leakHarness) post(trigger string, facts *wire.ReportFacts, checks ...wire.Check) {
	h.t.Helper()
	h.seq++
	ts := h.base.Add(time.Duration(h.seq) * time.Second)
	body, _ := json.Marshal(wire.Report{Timestamp: ts, AgentVersion: "test", Trigger: trigger, Checks: checks, Facts: facts})
	req, _ := http.NewRequest("POST", h.url+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("report: %d %s", resp.StatusCode, b)
	}
}

func (h *leakHarness) uid() int64 { return mustUserID(h.t, h.d, "leakrouter") }

func (h *leakHarness) leakRows() []db.EventRow {
	h.t.Helper()
	rows, err := h.d.Events().RecentEvents(h.uid(), bypassLeakCheck, 50)
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

func (h *leakHarness) leakDispatches() int {
	h.disp.mu.Lock()
	defer h.disp.mu.Unlock()
	n := 0
	for _, c := range h.disp.checks {
		if c.Name == bypassLeakCheck {
			n++
		}
	}
	return n
}

// Отчёт сломанного роутера: обход идёт в «Франкфурт», адрес не меняется.
// Замер выхода -- probeAt; external_reach -- reach.
func leakReport(h *leakHarness, probeAt time.Time, reach string, withFacts bool) {
	h.t.Helper()
	var facts *wire.ReportFacts
	if withFacts {
		facts = &wire.ReportFacts{Exit: leakExit(probeAt, map[string]wire.ExitProbe{
			"awg12": leakProbe(boolp(false), "203.0.113.7", "203.0.113.7", probeAt)})}
	}
	h.post("", facts,
		heartbeatCheck,
		leakTunnel("awg12", "Франкфурт", "running", 0),
		leakHydra(true, leakPolicy("awg12", 40)),
		wire.Check{Name: "external_reach", Status: reach},
	)
}

func TestReportBypassLeakQuietModeWritesRowsButNoAlerts(t *testing.T) {
	h := newLeakHarness(t, false)
	p1 := time.Now().UTC().Add(-30 * time.Minute)
	p2 := p1.Add(20 * time.Minute)
	leakReport(h, p1, "ok", true)
	leakReport(h, p1, "ok", false) // факт не пришёл -- берётся сохранённый
	leakReport(h, p2, "ok", true)
	leakReport(h, p2, "ok", false)
	rows := h.leakRows()
	if len(rows) != 4 {
		t.Fatalf("want 4 строки bypass_leak (одна на отчёт), got %d", len(rows))
	}
	if rows[0].Status != "fail" || rows[1].Status != "fail" || rows[2].Status != "ok" {
		t.Fatalf("порог: want свежие fail,fail затем ok, got %s,%s,%s", rows[0].Status, rows[1].Status, rows[2].Status)
	}
	if n := h.leakDispatches(); n != 0 {
		t.Fatalf("тихий режим: автомат тревог получил %d строк bypass_leak", n)
	}
	if st, _ := h.d.State().Get(h.uid(), bypassLeakCheck); st.CurrentStatus == "hard" || st.ConsecutiveFails != 0 || st.ConsecutiveOKs != 0 {
		t.Fatalf("тихий режим: состояние автомата %+v", st)
	}
}

func TestReportBypassLeakEnabledGoesHard(t *testing.T) {
	h := newLeakHarness(t, true)
	p1 := time.Now().UTC().Add(-30 * time.Minute)
	p2 := p1.Add(20 * time.Minute)
	leakReport(h, p1, "ok", true)
	leakReport(h, p1, "ok", false)
	leakReport(h, p2, "ok", true)
	leakReport(h, p2, "ok", false)
	if st, _ := h.d.State().Get(h.uid(), bypassLeakCheck); st.CurrentStatus != "hard" {
		t.Fatalf("включённый режим: want hard, got %+v", st)
	}
	h.disp.mu.Lock()
	defer h.disp.mu.Unlock()
	last := h.disp.checks[len(h.disp.checks)-1]
	if h.disp.calls[len(h.disp.calls)-1] != state.Hard || last.Name != bypassLeakCheck {
		t.Fatalf("тревога не ушла: %v %#v", h.disp.calls, last)
	}
	if last.Details["tunnel_name"] != "Франкфурт" || last.Details["external_reach"] != "ok" {
		t.Fatalf("тревоге не хватает имени или ответа доступности: %#v", last.Details)
	}
}

func TestReportBypassLeakSkipsHookAndAgentRows(t *testing.T) {
	h := newLeakHarness(t, false)
	p1 := time.Now().UTC().Add(-5 * time.Minute)
	h.post(wire.TriggerHook, nil, heartbeatCheck, leakTunnel("awg12", "Франкфурт", "running", 0),
		leakHydra(true, leakPolicy("awg12", 40)))
	if rows := h.leakRows(); len(rows) != 0 {
		t.Fatalf("хук-отчёт записал строку bypass_leak: %+v", rows)
	}
	// Строку с этим именем пишет только бэкенд: присланную агентом заменяет свой вердикт.
	h.post("", &wire.ReportFacts{Exit: leakExit(p1, map[string]wire.ExitProbe{
		"awg12": leakProbe(boolp(true), "198.51.100.9", "203.0.113.7", p1)})},
		heartbeatCheck, leakTunnel("awg12", "Франкфурт", "running", 0),
		leakHydra(true, leakPolicy("awg12", 40)),
		wire.Check{Name: bypassLeakCheck, Status: "fail"})
	rows := h.leakRows()
	if len(rows) != 1 || rows[0].Status != "ok" || !strings.Contains(rows[0].DetailsJSON, `"observed":"match"`) {
		t.Fatalf("want одна строка бэкенда ok/match, got %+v", rows)
	}
}

// --- экраны в тихом режиме ---

func leakScreens(t *testing.T, enabled bool) (checks []string, timeline []string) {
	t.Helper()
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	ts := time.Now().UTC().Add(-2 * time.Minute)
	if err := d.Events().Insert(ownedID, "agent_heartbeat", "ok", "", ts); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(ownedID, bypassLeakCheck, "fail", `{"tunnel_id":"awg12"}`, ts); err != nil {
		t.Fatal(err)
	}
	h := NewMux(Deps{DB: d, TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999,
		AlertPolicy: AlertPolicy{BypassLeakEnabled: enabled}})
	get := func(path string, into any) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", telegramUserID))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatal(err)
		}
	}
	var ev miniappRouterEventsResp
	get(fmt.Sprintf("/v1/miniapp/routers/%d/events", ownedID), &ev)
	for _, c := range ev.Checks {
		checks = append(checks, c.CheckName)
	}
	var tl miniappTimelineResp
	get(fmt.Sprintf("/v1/miniapp/routers/%d/timeline?raw=1", ownedID), &tl)
	for _, e := range tl.Events {
		timeline = append(timeline, e.CheckName)
	}
	return checks, timeline
}

func TestMiniappHidesBypassLeakInQuietMode(t *testing.T) {
	checks, timeline := leakScreens(t, false)
	for _, n := range append(checks, timeline...) {
		if n == bypassLeakCheck {
			t.Fatalf("тихий режим: строка bypass_leak на экране: checks=%v timeline=%v", checks, timeline)
		}
	}
	checks, _ = leakScreens(t, true)
	found := false
	for _, n := range checks {
		found = found || n == bypassLeakCheck
	}
	if !found {
		t.Fatalf("включённый режим: строки bypass_leak нет: %v", checks)
	}
}

// --- образец подсчёта за неделю (копия -- в docs/operations, вне git) ---

func TestBypassLeakWeeklyCountSQL(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	a, _ := d.Users().Insert("leak-a", "7171717171717171717171717171717171717171717171717171717171717171", "198.51.100.12", "awg0")
	b, _ := d.Users().Insert("leak-b", "7272727272727272727272727272727272727272727272727272727272727272", "198.51.100.13", "awg0")
	now := time.Now().UTC()
	ins := func(uid int64, status, details string, ts time.Time) {
		if err := d.Events().Insert(uid, bypassLeakCheck, status, details, ts); err != nil {
			t.Fatal(err)
		}
	}
	ins(a, "ok", `{"observed":"mismatch"}`, now.Add(-3*time.Hour))
	ins(a, "fail", `{"observed":"mismatch"}`, now.Add(-2*time.Hour))
	ins(a, "fail", `{"observed":"mismatch"}`, now.Add(-time.Hour))
	ins(a, "fail", `{"observed":"mismatch"}`, now.Add(-10*24*time.Hour)) // старше недели
	ins(b, "ok", `{"unverified":true,"reason":"singbox"}`, now.Add(-time.Hour))
	ins(b, "ok", `{"observed":"match"}`, now.Add(-30*time.Minute))
	if err := d.Events().Insert(a, "dns", "fail", "", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	since := now.Add(-7 * 24 * time.Hour)
	rows, err := d.SQL().Query(bypassLeakWeeklySQL, since, since, since)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string][3]int{}
	for rows.Next() {
		var nick string
		var reports, fails, unverified int
		if err := rows.Scan(&nick, &reports, &fails, &unverified); err != nil {
			t.Fatal(err)
		}
		got[nick] = [3]int{reports, fails, unverified}
	}
	if got["leak-a"] != [3]int{3, 2, 0} || got["leak-b"] != [3]int{2, 0, 1} {
		t.Fatalf("подсчёт: %v", got)
	}

	// Таблица горячая: подсчёт обязан идти по индексу (user_id, check_name, ts).
	plan, err := d.SQL().Query("EXPLAIN QUERY PLAN "+bypassLeakWeeklySQL, since, since, since)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	var steps []string
	for plan.Next() {
		var id, parent, notused sql.NullInt64
		var detail string
		if err := plan.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		steps = append(steps, detail)
	}
	for _, s := range steps {
		if strings.HasPrefix(s, "SCAN e") || strings.HasPrefix(s, "SCAN events") {
			t.Fatalf("полный проход по events: %v", steps)
		}
	}
}
