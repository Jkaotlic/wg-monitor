package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// agentScriptSink -- очередь с «роутером»: на команду, для которой answer
// вернул ok=true, результат появляется сразу. answer == nil -- роутер молчит.
type agentScriptSink struct {
	dashboardActionSink
	answer func(cmd wire.Command) (wire.CommandResult, bool)
	// recorded -- когда «очередь» записала результат; нет записи -- только что.
	recorded map[string]time.Time
}

func (s *agentScriptSink) ResultRecordedAt(_ int64, id string) (time.Time, bool) {
	if at, ok := s.recorded[id]; ok {
		return at, true
	}
	if _, ok := s.results[id]; ok {
		return time.Now(), true
	}
	return time.Time{}, false
}

func (s *agentScriptSink) Enqueue(userID int64, cmd wire.Command) error {
	if err := s.dashboardActionSink.Enqueue(userID, cmd); err != nil {
		return err
	}
	if s.answer == nil {
		return nil
	}
	res, ok := s.answer(cmd)
	if !ok {
		return nil
	}
	res.ID = cmd.ID
	if s.results == nil {
		s.results = map[string]wire.CommandResult{}
	}
	s.results[cmd.ID] = res
	return nil
}

func (s *agentScriptSink) actions() []string {
	out := make([]string, 0, len(s.enqueued))
	for _, c := range s.enqueued {
		out = append(out, c.Action)
	}
	return out
}

func newTunnelEnv(t *testing.T, answer func(wire.Command) (wire.CommandResult, bool)) (*cabinetEnv, *agentScriptSink) {
	t.Helper()
	sink := &agentScriptSink{answer: answer}
	env := newCabinetEnv(t, func(d *Deps) { d.CommandSink = sink })
	return env, sink
}

func tunnelStateBody(t *testing.T, rec *httptest.ResponseRecorder) miniappTunnelStateResp {
	t.Helper()
	var body miniappTunnelStateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("ответ не JSON: %d %s", rec.Code, rec.Body.String())
	}
	return body
}

// Снимок роутера: правила на vpn-nl (свои) и на vpn-de (через политику),
// пустой vpn-spare, главный выход vpn-main, старый awg3 и подключение
// провайдера, которое VPN-туннелем не является.
func tunnelDeleteSnapshot() wire.RouteSnapshot {
	return wire.RouteSnapshot{
		HRNeo: wire.HRStatus{Installed: true, Running: true},
		Tunnels: []wire.TunnelMeta{
			{ID: "awg12", Name: "vpn-nl", Iface: "nwg12", Type: "managed", Enabled: true},
			{ID: "awg10", Name: "vpn-de", Iface: "nwg10", Type: "managed", Enabled: true},
			{ID: "awg14", Name: "vpn-spare", Iface: "nwg14", Type: "managed", Enabled: true},
			{ID: "awg16", Name: "vpn-main", Iface: "nwg16", Type: "managed", Enabled: true},
			{ID: "awg3", Name: "", Iface: "nwg3", Type: "managed"},
			{ID: "tun5", Name: "vpn-opkg", Iface: "opkgtun5", Type: "managed", Enabled: true},
			{ID: "ISP", Name: "Провайдер", Iface: "ISP", Type: "ndms", Enabled: true},
		},
		Counts:        map[string]wire.TunnelCounts{"awg12": {DNS: 2, Static: 1, HRNeo: 1}},
		Policies:      []wire.RoutePolicySummary{{Name: "HydraRoute", DNS: 5, HRNeo: 5, ActiveTunnelID: "awg10", ViaVPN: true}},
		DefaultEgress: "awg16",
		PolicyModel:   true,
	}
}

func routeStatusAnswer(snap wire.RouteSnapshot) func(wire.Command) (wire.CommandResult, bool) {
	return func(cmd wire.Command) (wire.CommandResult, bool) {
		if cmd.Action != "route_status" {
			return wire.CommandResult{}, false
		}
		b, _ := json.Marshal(snap)
		return wire.CommandResult{Status: "ok", Output: string(b)}, true
	}
}

func postTunnelDelete(t *testing.T, env *cabinetEnv, tgUser int64, tunnelID, confirm string) *httptest.ResponseRecorder {
	t.Helper()
	return env.do(t, tgUser, http.MethodPost, "/v1/miniapp/routers/{id}/tunnels/"+tunnelID+"/delete", `{"confirm":`+strconv.Quote(confirm)+`}`)
}

// Решение 1: удаление -- админ и владелец. Оператору и постороннему -- 404,
// и роутеру ничего не уходит, даже route_status.
func TestMiniappTunnelDeleteGateByRole(t *testing.T) {
	for _, tc := range []struct {
		user    int64
		allowed bool
	}{{cabOwner, true}, {cabAdmin, true}, {cabOperator, false}, {777, false}} {
		env, sink := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
		rec := postTunnelDelete(t, env, tc.user, "awg14", "vpn-spare")
		if !tc.allowed {
			if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
				t.Errorf("user %d: %d %s", tc.user, rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 0 {
				t.Errorf("user %d: роутеру ушло %v", tc.user, sink.actions())
			}
			continue
		}
		if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "queued" {
			t.Errorf("user %d: %d %s", tc.user, rec.Code, rec.Body.String())
		}
	}
}

func TestMiniappTunnelDeleteQueuesEmptyTunnelAfterFreshSnapshot(t *testing.T) {
	env, sink := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
	rec := postTunnelDelete(t, env, cabOwner, "awg14", "  VPN‑SPARE ")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	body := tunnelStateBody(t, rec)
	if got := sink.actions(); len(got) != 2 || got[0] != "route_status" || got[1] != "tunnel_delete" {
		t.Fatalf("очередь: %v", got)
	}
	del := sink.enqueued[1]
	if body.State != "queued" || body.CmdID != del.ID || del.ID == "" {
		t.Fatalf("ответ %+v, команда %+v", body, del)
	}
	// awg14 -- идентификатор вида awgN, поэтому с принудительной чисткой.
	if len(del.Args) != 2 || del.Args["tunnel_id"] != "awg14" || del.Args["force_legacy_cleanup"] != true {
		t.Fatalf("аргументы удаления: %+v", del.Args)
	}

	// Второе удаление спрашивает роутер заново, а не верит прошлому снимку.
	rec = postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("второе: %d %s", rec.Code, rec.Body.String())
	}
	if got := sink.actions(); len(got) != 4 || got[2] != "route_status" {
		t.Fatalf("второе удаление без свежего снимка: %v", got)
	}
}

// Идентификаторы awgN удаляются с принудительной чисткой -- как в боте;
// остальные -- без неё.
func TestMiniappTunnelDeleteLegacyAWGForcesCleanup(t *testing.T) {
	env, sink := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
	// Имени у awg3 нет -- набирают идентификатор.
	rec := postTunnelDelete(t, env, cabOwner, "awg3", "awg3")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if args := sink.enqueued[1].Args; args["force_legacy_cleanup"] != true || args["tunnel_id"] != "awg3" {
		t.Fatalf("аргументы: %+v", args)
	}
	rec = postTunnelDelete(t, env, cabOwner, "tun5", "vpn-opkg")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("tun5: %d %s", rec.Code, rec.Body.String())
	}
	if args := sink.enqueued[3].Args; len(args) != 1 || args["tunnel_id"] != "tun5" {
		t.Fatalf("tun5 без чистки: %+v", args)
	}
	if !miniappLegacyAWGTunnelID("awg3") || miniappLegacyAWGTunnelID("awg") || miniappLegacyAWGTunnelID("nwg3") || miniappLegacyAWGTunnelID("awg3x") {
		t.Fatal("правило старого идентификатора")
	}
}

// Решение 2: туннель с правилами не удаляется -- сервер проверяет сам и
// говорит число. Главный выход не удаляется вовсе.
func TestMiniappTunnelDeleteRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, tunnel, confirm string
		status                int
		code                  string
		mutate                func(*wire.RouteSnapshot)
	}{
		{"свои правила", "awg12", "vpn-nl", http.StatusConflict, "tunnel_has_rules", nil},
		{"правила политики", "awg10", "vpn-de", http.StatusConflict, "tunnel_has_rules", nil},
		{"главный выход", "awg16", "vpn-main", http.StatusConflict, "tunnel_is_default", nil},
		{"неполный снимок", "awg14", "vpn-spare", http.StatusConflict, "snapshot_partial",
			func(s *wire.RouteSnapshot) { s.Warnings = []string{"/api/routing/access-policies failed: 500"} }},
		{"не набрано имя", "awg14", "vpn-spar", http.StatusBadRequest, "confirm_mismatch", nil},
		{"нет такого", "awg99", "awg99", http.StatusNotFound, "tunnel_not_found", nil},
		{"не VPN-туннель", "ISP", "Провайдер", http.StatusConflict, "tunnel_not_managed", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := tunnelDeleteSnapshot()
			if tc.mutate != nil {
				tc.mutate(&snap)
			}
			env, sink := newTunnelEnv(t, routeStatusAnswer(snap))
			rec := postTunnelDelete(t, env, cabOwner, tc.tunnel, tc.confirm)
			code, message, _ := cabinetErrorBody(t, rec)
			if rec.Code != tc.status || code != tc.code || message == "" {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			for _, a := range sink.actions() {
				if a == "tunnel_delete" {
					t.Fatalf("удаление ушло роутеру: %v", sink.actions())
				}
			}
		})
	}
}

func TestMiniappTunnelDeleteHasRulesCarriesBreakdown(t *testing.T) {
	env, _ := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
	rec := postTunnelDelete(t, env, cabOwner, "awg12", "vpn-nl")
	var body struct {
		Code    string             `json:"code"`
		Message string             `json:"message"`
		Rules   miniappTunnelRules `json:"rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Rules != (miniappTunnelRules{Total: 3, DNS: 2, Static: 1, HRNeo: 1}) {
		t.Fatalf("rules: %+v", body.Rules)
	}
	if !strings.Contains(body.Message, "3 правила") || !strings.Contains(body.Message, "перенесите") {
		t.Fatalf("message: %q", body.Message)
	}
}

// Идентификатор проверяется до вопроса роутеру: «__other__» -- не туннель.
func TestMiniappTunnelDeleteRejectsBadID(t *testing.T) {
	env, sink := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
	for _, id := range []string{wire.RouteOtherID, "a%20b"} {
		rec := postTunnelDelete(t, env, cabOwner, id, "x")
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadRequest || code != "invalid_tunnel_id" {
			t.Errorf("%q: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("роутеру ушло: %v", sink.actions())
	}
}

// Роутер не успел ответить -- «ещё проверяю», и повтор того же запроса не
// ставит второй route_status. Когда ответ приходит, повтор решает.
func TestMiniappTunnelDeleteCheckingThenQueued(t *testing.T) {
	env, sink := newTunnelEnv(t, nil)
	for i := 0; i < 2; i++ {
		rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
		if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "checking" {
			t.Fatalf("повтор %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := sink.actions(); len(got) != 1 || got[0] != "route_status" {
		t.Fatalf("повторы поставили лишнее: %v", got)
	}
	b, _ := json.Marshal(tunnelDeleteSnapshot())
	sink.results = map[string]wire.CommandResult{sink.enqueued[0].ID: {ID: sink.enqueued[0].ID, Status: "ok", Output: string(b)}}
	rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
	if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "queued" {
		t.Fatalf("после ответа: %d %s", rec.Code, rec.Body.String())
	}
	if got := sink.actions(); len(got) != 2 || got[1] != "tunnel_delete" {
		t.Fatalf("очередь: %v", got)
	}
}

func TestMiniappTunnelDeleteRouterErrors(t *testing.T) {
	for _, tc := range []struct {
		res  wire.CommandResult
		code string
	}{
		{wire.CommandResult{Status: "err", Output: "awgmgr client not configured"}, "router_failed"},
		{wire.CommandResult{Status: "ok", Output: "not json"}, "router_garbled"},
	} {
		res := tc.res
		env, sink := newTunnelEnv(t, func(wire.Command) (wire.CommandResult, bool) { return res, true })
		rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
		if code, message, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadGateway || code != tc.code || strings.Contains(message, "awgmgr") {
			t.Errorf("%s: %d %s", tc.code, rec.Code, rec.Body.String())
		}
		if len(sink.enqueued) != 1 {
			t.Errorf("%s: очередь %v", tc.code, sink.actions())
		}
	}
}

func TestMiniappTunnelDeleteWithoutQueue(t *testing.T) {
	env := newCabinetEnv(t, func(d *Deps) { d.CommandSink = nil })
	rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "commands_not_configured" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Ревью цикла 4: упавший VPN-туннель, стоящий в цепочке политики РАНЬШЕ
// активного звена (или активного звена нет вовсе), правил по формуле экрана
// не несёт -- но после удаления политика навсегда останется на WAN. Такой
// туннель не удаляется: отказ tunnel_in_policy_chain с числом правил
// политики. Резервное звено ПОСЛЕ активного удаление не блокирует.
func policyChainSnapshot(ifaces []wire.RoutePolicyInterface, active string) wire.RouteSnapshot {
	snap := tunnelDeleteSnapshot()
	snap.Tunnels = append(snap.Tunnels, wire.TunnelMeta{ID: "awg18", Name: "vpn-x", Iface: "nwg18", Type: "managed", Enabled: true, Status: "down"})
	snap.Policies = []wire.RoutePolicySummary{{Name: "Семья", DNS: 5, HRNeo: 2, ActiveTunnelID: active, Interfaces: ifaces}}
	return snap
}

func TestMiniappTunnelDeleteRefusesTunnelAheadInPolicyChain(t *testing.T) {
	cases := []struct {
		name   string
		snap   wire.RouteSnapshot
		refuse bool
	}{
		{"упал первым, активен WAN", policyChainSnapshot([]wire.RoutePolicyInterface{
			{Bind: "nwg18", TunnelID: "awg18", Role: "unavailable", Order: 1},
			{Bind: "ISP", Role: "active", Order: 2},
		}, ""), true},
		{"упал первым, активно запасное звено", policyChainSnapshot([]wire.RoutePolicyInterface{
			{Bind: "nwg18", TunnelID: "awg18", Role: "unavailable", Order: 1},
			{Bind: "nwg14", TunnelID: "awg14", Role: "active", Order: 2},
		}, "awg14"), true},
		{"единственное звено, активного нет", policyChainSnapshot([]wire.RoutePolicyInterface{
			{Bind: "nwg18", TunnelID: "awg18", Role: "unavailable", Order: 1},
		}, ""), true},
		{"резерв после активного", policyChainSnapshot([]wire.RoutePolicyInterface{
			{Bind: "nwg14", TunnelID: "awg14", Role: "active", Order: 1},
			{Bind: "nwg18", TunnelID: "awg18", Role: "fallback", Order: 2},
		}, "awg14"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, sink := newTunnelEnv(t, routeStatusAnswer(tc.snap))
			rec := postTunnelDelete(t, env, cabOwner, "awg18", "vpn-x")
			if !tc.refuse {
				if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "queued" {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				return
			}
			var body struct {
				Code    string             `json:"code"`
				Error   string             `json:"error"`
				Message string             `json:"message"`
				Rules   miniappTunnelRules `json:"rules"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusConflict || body.Code != "tunnel_in_policy_chain" || body.Error != body.Code ||
				body.Rules.Total != 5 || !strings.Contains(body.Message, "«vpn-x»") || !strings.Contains(body.Message, "мастер замены") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			for _, a := range sink.actions() {
				if a == "tunnel_delete" {
					t.Fatalf("удаление ушло роутеру: %v", sink.actions())
				}
			}
		})
	}
}

// Ревью цикла 4: роутер не назвал главный выход (default_egress пуст) -- тогда
// главным считается единственный включённый не лежащий VPN-туннель с
// default_route (правило routingVerdict в routes.js). Претендентов больше
// одного или он лежит -- выход не определён, удаление не блокируется.
func TestMiniappTunnelDeleteEmptyEgressSingleClaimant(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*wire.RouteSnapshot)
		refuse bool
	}{
		{"единственный претендент", func(s *wire.RouteSnapshot) { s.Tunnels[2].DefaultRoute, s.Tunnels[2].Status = true, "up" }, true},
		{"статус неизвестен", func(s *wire.RouteSnapshot) { s.Tunnels[2].DefaultRoute = true }, true},
		{"претендент лежит", func(s *wire.RouteSnapshot) { s.Tunnels[2].DefaultRoute, s.Tunnels[2].Status = true, "down" }, false},
		{"претендент выключен", func(s *wire.RouteSnapshot) { s.Tunnels[2].DefaultRoute, s.Tunnels[2].Enabled = true, false }, false},
		{"двое претендентов", func(s *wire.RouteSnapshot) {
			s.Tunnels[2].DefaultRoute = true
			s.Tunnels[3].DefaultRoute = true
		}, false},
		{"претендент другой", func(s *wire.RouteSnapshot) { s.Tunnels[3].DefaultRoute = true }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := tunnelDeleteSnapshot()
			snap.DefaultEgress = ""
			tc.mutate(&snap)
			env, _ := newTunnelEnv(t, routeStatusAnswer(snap))
			rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
			if tc.refuse {
				if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "tunnel_is_default" {
					t.Fatalf("%d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "queued" {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	// default_egress == "direct" -- роутер сказал сам: флаги не решают.
	snap := tunnelDeleteSnapshot()
	snap.DefaultEgress = wire.DefaultEgressDirect
	snap.Tunnels[2].DefaultRoute, snap.Tunnels[2].Status = true, "up"
	env, _ := newTunnelEnv(t, routeStatusAnswer(snap))
	if rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare"); rec.Code != http.StatusAccepted {
		t.Fatalf("direct: %d %s", rec.Code, rec.Body.String())
	}
}

// Ревью цикла 4: агент не кладёт в снимок туннель без интерфейса, и отличить
// его от уже удалённого нельзя -- текст говорит, где удалить руками.
func TestMiniappTunnelDeleteNotFoundPointsToAWGManager(t *testing.T) {
	env, _ := newTunnelEnv(t, routeStatusAnswer(tunnelDeleteSnapshot()))
	rec := postTunnelDelete(t, env, cabOwner, "awg99", "awg99")
	if code, message, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "tunnel_not_found" ||
		!strings.Contains(message, "не сообщает") || !strings.Contains(message, "awg-manager") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

// Ревью цикла 4: решение об удалении -- только по снимку не старше 30 секунд.
// Ответ, пролежавший дольше (клиент вернулся к «проверяю» через минуту),
// спрашивается заново; а сама команда удаления живёт в очереди 2 минуты, а не
// 15: проснувшийся роутер не должен удалять по давно устаревшей проверке.
func TestMiniappTunnelDeleteStaleSnapshotAsksAgain(t *testing.T) {
	env, sink := newTunnelEnv(t, nil)
	if rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare"); tunnelStateBody(t, rec).State != "checking" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	b, _ := json.Marshal(tunnelDeleteSnapshot())
	first := sink.enqueued[0].ID
	sink.results = map[string]wire.CommandResult{first: {ID: first, Status: "ok", Output: string(b)}}
	sink.recorded = map[string]time.Time{first: time.Now().Add(-time.Minute)}
	rec := postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
	if rec.Code != http.StatusAccepted || tunnelStateBody(t, rec).State != "checking" {
		t.Fatalf("устаревший снимок: %d %s", rec.Code, rec.Body.String())
	}
	if got := sink.actions(); len(got) != 2 || got[1] != "route_status" {
		t.Fatalf("ждали новый вопрос: %v", got)
	}
	second := sink.enqueued[1].ID
	sink.results[second] = wire.CommandResult{ID: second, Status: "ok", Output: string(b)}
	rec = postTunnelDelete(t, env, cabOwner, "awg14", "vpn-spare")
	body := tunnelStateBody(t, rec)
	if rec.Code != http.StatusAccepted || body.State != "queued" {
		t.Fatalf("свежий снимок: %d %s", rec.Code, rec.Body.String())
	}
	del := sink.enqueued[len(sink.enqueued)-1]
	if del.Action != "tunnel_delete" || del.IssuedAt.IsZero() || del.ExpiresAt.Sub(del.IssuedAt) != 2*time.Minute {
		t.Fatalf("команда: %s issued=%v expires=%v", del.Action, del.IssuedAt, del.ExpiresAt)
	}
	// Роутер в тестовом парке не на связи: окно ожидания -- те же 2 минуты.
	if body.RouterAsleep && body.WakeWindowMin != 2 {
		t.Fatalf("окно ожидания: %+v", body)
	}
}
