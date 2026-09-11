package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Два VPN-туннеля и провайдер. Главный выход -- напрямую, если тест не
// скажет иначе; HydraRoute Neo запущен.
func lookupInputs(rules ...awgmgr.DNSRoute) routeInputs {
	return routeInputs{
		hr: &awgmgr.HydraRouteStatus{Installed: true, Running: true},
		tunnels: &awgmgr.TunnelsAll{Tunnels: []awgmgr.Tunnel{
			{ID: "awg1", Name: "vpn-nl", InterfaceName: "opkgtun1", Enabled: true},
			{ID: "awg2", Name: "vpn-reserve", InterfaceName: "opkgtun2", Enabled: true},
		}},
		routing: []awgmgr.RoutingTunnel{
			{ID: "wan-eth3", Name: "Провайдер", Iface: "eth3", Type: "wan", Status: "up", Available: true},
		},
		dns:      rules,
		settings: &awgmgr.Settings{Download: awgmgr.SettingsDownload{RouteTag: wire.DefaultEgressDirect}},
	}
}

func boundTo(iface string) []awgmgr.DNSRouteEntry {
	return []awgmgr.DNSRouteEntry{{Interface: iface, TunnelID: iface}}
}

// noExpand -- для правил без гео-тегов: любая развёртка там лишняя.
func noExpand(t *testing.T) func(string) ([]string, error) {
	t.Helper()
	return func(tag string) ([]string, error) {
		t.Errorf("развёртка %q не ожидалась", tag)
		return nil, errors.New("unexpected expand")
	}
}

func geoLists(lists map[string][]string) func(string) ([]string, error) {
	return func(tag string) ([]string, error) {
		lines, ok := lists[tag]
		if !ok {
			return nil, fmt.Errorf("no such tag %s", tag)
		}
		return lines, nil
	}
}

func TestLookupRoute_PlainDomainRule(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "ndms:work", Name: "Работа", Backend: "ndms", Enabled: true,
		Domains: []string{"example.com"}, Routes: boundTo("opkgtun1"),
	})
	res := lookupRoute("chat.example.com", in, noExpand(t))
	if res.Verdict != wire.LookupViaTunnel || res.TunnelID != "awg1" || res.TunnelName != "vpn-nl" || res.ByDefault {
		t.Fatalf("res = %+v", res)
	}
	if res.Domain != "chat.example.com" {
		t.Fatalf("domain = %q", res.Domain)
	}
	want := wire.RouteLookupMatch{RuleName: "Работа", Pattern: "example.com", Via: wire.LookupViaTunnel, TunnelID: "awg1", TunnelName: "vpn-nl"}
	if len(res.Matches) != 1 || res.Matches[0] != want {
		t.Fatalf("matches = %+v", res.Matches)
	}
	// Правило про example.com не называет example.org и не называет
	// notexample.com: совпадение -- по границе метки, а не по подстроке.
	for _, other := range []string{"example.org", "notexample.com"} {
		if got := lookupRoute(other, in, noExpand(t)); !got.ByDefault {
			t.Errorf("%s: %+v", other, got)
		}
	}
}

func TestLookupRoute_GeositeSuffix(t *testing.T) {
	var asked []string
	expand := func(tag string) ([]string, error) {
		asked = append(asked, tag)
		return []string{".anthropic.com", ".clau.de", ".claude.ai"}, nil
	}
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:AI", Name: "Все AI сервисы", Backend: "hydraroute", Enabled: true,
		Domains: []string{"geosite:ANTHROPIC"}, Routes: boundTo("opkgtun1"),
	})
	res := lookupRoute("api.claude.ai", in, expand)
	if res.Verdict != wire.LookupViaTunnel || res.TunnelName != "vpn-nl" || res.ByDefault {
		t.Fatalf("res = %+v", res)
	}
	if len(res.Matches) != 1 || res.Matches[0].Pattern != "geosite:ANTHROPIC" || res.Matches[0].RuleName != "Все AI сервисы" {
		t.Fatalf("matches = %+v", res.Matches)
	}
	if !slices.Equal(asked, []string{"ANTHROPIC"}) {
		t.Fatalf("развёрнуто %v", asked)
	}
	// Сам домен списка тоже его: ".claude.ai" -- это claude.ai и всё под ним.
	if got := lookupRoute("claude.ai", in, expand); got.Verdict != wire.LookupViaTunnel {
		t.Fatalf("claude.ai: %+v", got)
	}
	if got := lookupRoute("notclaude.ai", in, expand); !got.ByDefault {
		t.Fatalf("notclaude.ai: %+v", got)
	}
}

func TestLookupRoute_GeositeFullAndKeyword(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:Mix", Name: "Смесь", Backend: "hydraroute", Enabled: true,
		Domains: []string{"geosite:MIX"}, Routes: boundTo("opkgtun1"),
	})
	expand := geoLists(map[string][]string{"MIX": {
		"full:exact.example.com", "keyword:tracker", "domain:example.org",
	}})
	cases := map[string]bool{
		"exact.example.com":       true,  // full: -- точное совпадение
		"sub.exact.example.com":   false, // ...и только оно
		"ads-tracker.example.net": true,  // keyword: -- подстрока
		"www.example.org":         true,  // domain: -- домен и всё под ним
		"example.org":             true,
		"example.net":             false,
	}
	for host, want := range cases {
		res := lookupRoute(host, in, expand)
		if got := res.Verdict == wire.LookupViaTunnel && !res.ByDefault; got != want {
			t.Errorf("%s: совпало=%v, want %v (%+v)", host, got, want, res)
		}
	}
}

func TestLookupRoute_RegexpNoted(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:Re", Name: "Шаблоны", Backend: "hydraroute", Enabled: true,
		Domains: []string{"geosite:RE"}, Routes: boundTo("opkgtun1"),
	})
	res := lookupRoute("a.example.com", in, geoLists(map[string][]string{"RE": {`regexp:^.*\.example\.com$`}}))
	if !res.ByDefault || res.Verdict != wire.LookupViaDirect {
		t.Fatalf("шаблон не проверяется, значит и не совпадает: %+v", res)
	}
	if !slices.Contains(res.Notes, "regexp_unchecked") {
		t.Fatalf("notes = %v", res.Notes)
	}
}

func TestLookupRoute_NoMatchDirectDefault(t *testing.T) {
	res := lookupRoute("claude.ai", lookupInputs(), noExpand(t))
	if res.Verdict != wire.LookupViaDirect || !res.ByDefault || res.TunnelID != "" || len(res.Matches) != 0 {
		t.Fatalf("res = %+v", res)
	}
	// matches -- массив, а не null: экрану не надо гадать про форму.
	b, _ := json.Marshal(res)
	if !strings.Contains(string(b), `"matches":[]`) {
		t.Fatalf("json = %s", b)
	}
}

// Главный выход роутера -- VPN-туннель. Сайт без правил идёт через него, и
// ответ «напрямую» был бы ровно обратным правде.
func TestLookupRoute_NoMatchTunnelDefault(t *testing.T) {
	in := lookupInputs()
	in.settings = &awgmgr.Settings{Download: awgmgr.SettingsDownload{RouteTag: "awg-awg1", RouteKind: "awg"}}
	res := lookupRoute("claude.ai", in, noExpand(t))
	if res.Verdict != wire.LookupViaTunnel || !res.ByDefault || res.TunnelID != "awg1" || res.TunnelName != "vpn-nl" {
		t.Fatalf("res = %+v", res)
	}
}

// Роутер не назвал главный выход -- это ответ, и подменять его догадкой
// нельзя.
func TestLookupRoute_NoMatchUnknownDefault(t *testing.T) {
	in := lookupInputs()
	in.settings = nil
	res := lookupRoute("claude.ai", in, noExpand(t))
	if res.Verdict != wire.LookupViaUnknown || !res.ByDefault || res.TunnelID != "" {
		t.Fatalf("res = %+v", res)
	}
}

func TestLookupRoute_DisabledRuleIgnored(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "ndms:off", Name: "Выключено", Backend: "ndms", Enabled: false,
		Domains: []string{"example.com"}, Routes: boundTo("opkgtun1"),
	})
	res := lookupRoute("example.com", in, noExpand(t))
	if !res.ByDefault || res.Verdict != wire.LookupViaDirect || len(res.Matches) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestLookupRoute_HydraRouteStoppedNoted(t *testing.T) {
	in := lookupInputs(
		awgmgr.DNSRoute{
			ID: "hr:Work", Name: "Работа", Backend: "hydraroute", Enabled: true,
			Domains: []string{"example.com"}, Routes: boundTo("opkgtun1"),
		},
		awgmgr.DNSRoute{
			ID: "ndms:Net", Name: "Сеть", Backend: "ndms", Enabled: true,
			Domains: []string{"example.net"}, Routes: boundTo("opkgtun2"),
		},
	)
	in.hr = &awgmgr.HydraRouteStatus{Installed: true, Running: false}
	res := lookupRoute("chat.example.com", in, noExpand(t))
	if !res.ByDefault || res.Verdict != wire.LookupViaDirect {
		t.Fatalf("правило остановленного движка не действует: %+v", res)
	}
	if !slices.Contains(res.Notes, "hr_not_running") {
		t.Fatalf("notes = %v", res.Notes)
	}
	// Правила самого роутера (не HydraRoute Neo) от этого не зависят.
	if got := lookupRoute("example.net", in, noExpand(t)); got.Verdict != wire.LookupViaTunnel || got.TunnelName != "vpn-reserve" {
		t.Fatalf("example.net: %+v", got)
	}
}

func TestLookupRoute_MixedDestinations(t *testing.T) {
	in := lookupInputs(
		awgmgr.DNSRoute{
			ID: "ndms:A", Name: "Первое", Backend: "ndms", Enabled: true,
			Domains: []string{"example.com"}, Routes: boundTo("opkgtun1"),
		},
		awgmgr.DNSRoute{
			ID: "hr:B", Name: "Второе", Backend: "hydraroute", Enabled: true,
			Domains: []string{"geosite:CHAT"}, Routes: boundTo("opkgtun2"),
		},
	)
	res := lookupRoute("chat.example.com", in, geoLists(map[string][]string{"CHAT": {".chat.example.com"}}))
	if res.Verdict != wire.LookupMixed || res.ByDefault || res.TunnelID != "" || len(res.Matches) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if res.Matches[0].TunnelName != "vpn-nl" || res.Matches[1].TunnelName != "vpn-reserve" {
		t.Fatalf("matches = %+v", res.Matches)
	}

	// Два правила в одно место -- это одно место, а не «разные».
	in.dns[1].Routes = boundTo("opkgtun1")
	same := lookupRoute("chat.example.com", in, geoLists(map[string][]string{"CHAT": {".chat.example.com"}}))
	if same.Verdict != wire.LookupViaTunnel || same.TunnelName != "vpn-nl" || len(same.Matches) != 2 {
		t.Fatalf("same = %+v", same)
	}
}

func TestLookupRoute_GeoIPNoted(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:AIgeo", Name: "AIgeo", Backend: "hydraroute", Enabled: true,
		ManualDomains: []string{"geoip:ANTHROPIC"}, Subnets: []string{"geoip:ANTHROPIC"},
		Routes: boundTo("opkgtun1"),
	})
	res := lookupRoute("claude.ai", in, noExpand(t))
	if !res.ByDefault || !slices.Contains(res.Notes, "ip_rules_unchecked") {
		t.Fatalf("res = %+v", res)
	}

	// Подсеть в правиле по имени и правило по адресу -- то же самое: по имени
	// сайта их не проверить.
	in = lookupInputs(awgmgr.DNSRoute{
		ID: "ndms:Net", Name: "Сеть", Backend: "ndms", Enabled: true,
		Domains: []string{"203.0.113.0/24"}, Routes: boundTo("opkgtun1"),
	})
	if got := lookupRoute("claude.ai", in, noExpand(t)); !slices.Contains(got.Notes, "ip_rules_unchecked") {
		t.Fatalf("cidr: %+v", got)
	}
	in = lookupInputs()
	in.statics = []awgmgr.StaticRoute{{ID: "s1", Name: "Офис", TunnelID: "opkgtun1", Subnets: []string{"198.51.100.0/24"}, Enabled: true}}
	if got := lookupRoute("claude.ai", in, noExpand(t)); !slices.Contains(got.Notes, "ip_rules_unchecked") {
		t.Fatalf("static: %+v", got)
	}
}

func TestLookupRoute_ExpandFailureNoted(t *testing.T) {
	calls := 0
	expand := func(tag string) ([]string, error) {
		calls++
		return nil, errors.New("boom")
	}
	in := lookupInputs(
		awgmgr.DNSRoute{
			ID: "hr:A", Name: "Первое", Backend: "hydraroute", Enabled: true,
			Domains: []string{"geosite:BROKEN"}, Routes: boundTo("opkgtun1"),
		},
		awgmgr.DNSRoute{
			ID: "hr:B", Name: "Второе", Backend: "hydraroute", Enabled: true,
			Domains: []string{"geosite:BROKEN"}, Routes: boundTo("opkgtun2"),
		},
	)
	res := lookupRoute("claude.ai", in, expand)
	if !res.ByDefault || res.Verdict != wire.LookupViaDirect {
		t.Fatalf("не раскрытый список считается несовпавшим: %+v", res)
	}
	if !slices.Equal(res.Notes, []string{"geo_expand_failed:BROKEN"}) {
		t.Fatalf("notes = %v", res.Notes)
	}
	if calls != 1 {
		t.Fatalf("развёртка одного тега за вызов -- одна, было %d", calls)
	}
}

// sing-box решает сам, по своим правилам: ответ по правилам роутера был бы
// уверенной неправдой.
func TestLookupRoute_SingboxUnknown(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "ndms:work", Name: "Работа", Backend: "ndms", Enabled: true,
		Domains: []string{"example.com"}, Routes: boundTo("opkgtun1"),
	})
	in.settings.SingboxRouter.Enabled = true
	res := lookupRoute("example.com", in, noExpand(t))
	if res.Verdict != wire.LookupViaUnknown || res.TunnelID != "" || !slices.Equal(res.Notes, []string{"singbox_router"}) {
		t.Fatalf("res = %+v", res)
	}
}

// Правило, привязанное общим набором правил, идёт туда, куда ведёт первое
// живое звено набора -- тот же ответ, что у route_status.
func TestLookupRoute_PolicyBoundRuleFollowsActiveLink(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:AI", Name: "Все AI сервисы", Backend: "hydraroute", Enabled: true,
		Domains: []string{"claude.ai"}, HRPolicyName: "HydraRoute",
	})
	in.policies = []awgmgr.AccessPolicy{{Name: "HydraRoute", Interfaces: []awgmgr.AccessPolicyInterface{
		{Name: "OpkgTun2", Order: 0}, {Name: "OpkgTun1", Order: 1},
	}}}
	in.polIfaces = []awgmgr.PolicyInterface{{Name: "OpkgTun2", Up: false}, {Name: "OpkgTun1", Up: true}}
	res := lookupRoute("claude.ai", in, noExpand(t))
	if res.Verdict != wire.LookupViaTunnel || res.TunnelID != "awg1" || res.TunnelName != "vpn-nl" {
		t.Fatalf("res = %+v", res)
	}
}

// Наборы правил не прочитались -- куда ведёт привязанное ими правило, не
// знает никто, и туннель по умолчанию на эту роль не назначается.
func TestLookupRoute_PoliciesUnknownNoted(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:AI", Name: "Все AI сервисы", Backend: "hydraroute", Enabled: true,
		Domains: []string{"claude.ai"}, HRPolicyName: "HydraRoute",
	})
	in.policiesUnknown = true
	res := lookupRoute("claude.ai", in, noExpand(t))
	if res.Verdict != wire.LookupViaUnknown || res.ByDefault || len(res.Matches) != 1 || res.Matches[0].Via != wire.LookupViaUnknown {
		t.Fatalf("res = %+v", res)
	}
	if !slices.Contains(res.Notes, "policies_unknown") {
		t.Fatalf("notes = %v", res.Notes)
	}
}

// Правило, приколоченное к провайдеру, -- это «напрямую», а не «туннель eth3».
func TestLookupRoute_RuleToProviderIsDirect(t *testing.T) {
	in := lookupInputs(awgmgr.DNSRoute{
		ID: "hr:Bank", Name: "Банк", Backend: "hydraroute", Enabled: true,
		Domains: []string{"example.com"}, Routes: boundTo("eth3"),
	})
	in.settings = &awgmgr.Settings{Download: awgmgr.SettingsDownload{RouteTag: "awg-awg1", RouteKind: "awg"}}
	res := lookupRoute("example.com", in, noExpand(t))
	if res.Verdict != wire.LookupViaDirect || res.ByDefault || res.TunnelID != "" {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunner_RouteLookup_RejectsNonDomain(t *testing.T) {
	r := &Runner{AwgClient: awgmgr.New("http://unused.invalid")}
	for _, bad := range []string{"1.2.3.4/24", "", "geosite:ANTHROPIC"} {
		res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_lookup", Args: map[string]any{"domain": bad}})
		if res.Status != "err" || !strings.Contains(res.Output, "invalid domain") {
			t.Errorf("%q: status=%s output=%s", bad, res.Status, res.Output)
		}
	}
}

// Сквозной путь: раннер читает правила роутера и раскрывает гео-тег у самого
// awg-manager.
func TestRunner_RouteLookup_Dispatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":true,"running":true}}`))
	})
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg1","name":"vpn-nl","interfaceName":"opkgtun1","enabled":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:AI","name":"Все AI сервисы","backend":"hydraroute","enabled":true,
			 "domains":["geosite:ANTHROPIC"],"routes":[{"interface":"opkgtun1","tunnelId":"awg1"}]}
		]}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/hydraroute/geo-expand", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("kind") != "geosite" || r.URL.Query().Get("tag") != "ANTHROPIC" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"count":2,"lines":[".anthropic.com",".claude.ai"]}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &Runner{AwgClient: awgmgr.New(srv.URL)}
	res := r.Execute(context.Background(), wire.Command{ID: "x", Action: "route_lookup", Args: map[string]any{"domain": "API.Claude.AI."}})
	if res.Status != "ok" {
		t.Fatalf("status=%s output=%s", res.Status, res.Output)
	}
	var got wire.RouteLookupResult
	if err := json.Unmarshal([]byte(res.Output), &got); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, res.Output)
	}
	if got.Domain != "api.claude.ai" || got.Verdict != wire.LookupViaTunnel || got.TunnelName != "vpn-nl" {
		t.Fatalf("got = %+v", got)
	}
}
