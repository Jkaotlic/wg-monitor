package checks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const testYandexHost = "common.dot.dns.yandex.net"

func endpointsOf(eps ...keenetic.DNSEndpoint) func(context.Context) ([]keenetic.DNSEndpoint, error) {
	return func(context.Context) ([]keenetic.DNSEndpoint, error) { return eps, nil }
}

func resolvesOK(context.Context, string, string) ([]string, error) {
	return []string{"198.51.100.7"}, nil
}

func zonesOf(t *testing.T, got wire.Check) map[string]string {
	t.Helper()
	zones, ok := got.Details["zones"].(map[string]string)
	if !ok {
		t.Fatalf("zones не карта строк: %#v", got.Details["zones"])
	}
	return zones
}

// Проверка dns_split -- ЧИТАЮЩАЯ. Она не умеет FAIL вовсе: тревоги про DNS
// несут проверки dns и resolver_guard, и новая не имеет права ни разбудить
// человека ночью, ни попасть в счётчик тревог. Настройки не прочитались и
// роутер не резолвит -- это «неизвестно», а не поломка.
func TestDNSSplit_NothingReadableStaysOKAndSaysUnknown(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru", "su"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints: func(context.Context) ([]keenetic.DNSEndpoint, error) {
			return nil, errors.New("ndmc недоступен")
		},
		Resolve: func(context.Context, string, string) ([]string, error) {
			return nil, errors.New("сеть недоступна")
		},
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status = %q: эта проверка не умеет FAIL вовсе", got.Status)
	}
	zones := zonesOf(t, got)
	if len(zones) != 2 {
		t.Fatalf("зон в ответе %d, хотим 2: %#v", len(zones), zones)
	}
	for z, verdict := range zones {
		if verdict != "unknown" {
			t.Errorf("зона %s: %q, хотим unknown", z, verdict)
		}
	}
	if got.Details["resolves"] != "fail" {
		t.Errorf("resolves = %v, хотим fail", got.Details["resolves"])
	}
}

// Зона отдана Яндексу по DoT -- ровно то, что просил оператор (транспорт).
// Роутер мог записать строку в своей форме (регистр, точка в конце): узнаём
// по смыслу, а не по написанию.
func TestDNSSplit_ZoneOnYandexOverDoT(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru", "xn--p1ai", "su"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints: endpointsOf(
			keenetic.DNSEndpoint{Type: "dot", Host: "9.9.9.9", Port: 853, SNI: "dns.quad9.net"},
			keenetic.DNSEndpoint{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"},
			keenetic.DNSEndpoint{Type: "dot", Host: "Common.Dot.DNS.Yandex.net.", Port: 853, Zone: "XN--P1AI."},
			// Яндекс по адресу, узнаётся по имени сертификата.
			keenetic.DNSEndpoint{Type: "dot", Host: "77.88.8.8", Port: 853, SNI: testYandexHost, Zone: "su"},
		),
		Resolve: resolvesOK,
	}
	got := c.Run(context.Background(), Deps{})
	zones := zonesOf(t, got)
	for _, z := range []string{"ru", "xn--p1ai", "su"} {
		if zones[z] != "yandex_dot" {
			t.Errorf("зона %s: %q, хотим yandex_dot", z, zones[z])
		}
	}
	if got.Details["resolves"] != "ok" {
		t.Errorf("resolves = %v, хотим ok", got.Details["resolves"])
	}
}

// Остальные исходы различаются, потому что человеку чинить разное: Яндекс по
// DoH (транспорт не тот), зона у чужого резолвера, зона поделена между Яндексом
// и чужим (часть запросов уйдёт за границу), отдельного правила нет вовсе.
func TestDNSSplit_ZoneVerdictsAreDistinct(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru", "su", "tatar", "xn--p1acf"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints: endpointsOf(
			keenetic.DNSEndpoint{Type: "doh", URL: "https://" + testYandexHost + "/dns-query", Zone: "ru"},
			keenetic.DNSEndpoint{Type: "dot", Host: "1.1.1.1", Port: 853, Zone: "su"},
			keenetic.DNSEndpoint{Type: "dot", Host: testYandexHost, Port: 853, Zone: "tatar"},
			keenetic.DNSEndpoint{Type: "doh", URL: "https://dns.quad9.net/dns-query", Zone: "tatar"},
			// общий апстрим без зоны за правило для зоны не считается
			keenetic.DNSEndpoint{Type: "dot", Host: testYandexHost, Port: 853},
		),
		Resolve: resolvesOK,
	}
	zones := zonesOf(t, c.Run(context.Background(), Deps{}))
	want := map[string]string{"ru": "yandex_doh", "su": "other", "tatar": "mixed", "xn--p1acf": "none"}
	for z, w := range want {
		if zones[z] != w {
			t.Errorf("зона %s: %q, хотим %q", z, zones[z], w)
		}
	}
}

// Даже зона у чужого резолвера -- не FAIL: тревоги несут другие проверки.
func TestDNSSplit_BrokenSplitIsStillOK(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints:  endpointsOf(keenetic.DNSEndpoint{Type: "dot", Host: "1.1.1.1", Port: 853, Zone: "ru"}),
		Resolve:    resolvesOK,
	}
	if got := c.Run(context.Background(), Deps{}); got.Status != "ok" {
		t.Fatalf("status = %q: даже поломка раздельной схемы не даёт FAIL", got.Status)
	}
}

// Вердикт -- вывод по настройкам, а не замер. Слов уверенности в ответе быть не
// должно никогда.
func TestDNSSplit_NeverClaimsCertainty(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints:  endpointsOf(keenetic.DNSEndpoint{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"}),
		Resolve:    resolvesOK,
	}
	b, _ := json.Marshal(c.Run(context.Background(), Deps{}).Details)
	for _, word := range []string{"точно", "гарант", "доказан"} {
		if strings.Contains(string(b), word) {
			t.Errorf("проверка заявила уверенность (%q): %s", word, b)
		}
	}
}

// Проба живости спрашивает dns-proxy самого роутера, а не чужой сервер: важно,
// что увидит человек за этим роутером.
func TestDNSSplit_ResolveProbeAsksRouterProxy(t *testing.T) {
	var server, name string
	c := &DNSSplit{
		Zones:      []string{"ru"},
		YandexHost: testYandexHost,
		Canary:     "ya.ru",
		Endpoints:  endpointsOf(),
		Resolve: func(_ context.Context, s, n string) ([]string, error) {
			server, name = s, n
			return []string{"198.51.100.7"}, nil
		},
	}
	c.Run(context.Background(), Deps{})
	if server != localResolver || name != "ya.ru" {
		t.Errorf("спросили %q у %q, хотим ya.ru у %q", name, server, localResolver)
	}
}

// Маршрут до самого резолвера Яндекса берётся у route_lookup -- второго такого
// инструмента писать нельзя. Его неудача -- «неизвестно», а не выдумка.
func TestDNSSplit_RouteComesFromRouteLookupAndDegradesToUnknown(t *testing.T) {
	base := func(rl func(context.Context, string) (wire.RouteLookupResult, error)) *DNSSplit {
		return &DNSSplit{
			Zones:       []string{"ru"},
			YandexHost:  testYandexHost,
			Canary:      "ya.ru",
			Endpoints:   endpointsOf(),
			Resolve:     resolvesOK,
			RouteLookup: rl,
		}
	}
	got := base(func(context.Context, string) (wire.RouteLookupResult, error) {
		return wire.RouteLookupResult{Verdict: wire.LookupViaDirect}, nil
	}).Run(context.Background(), Deps{})
	if got.Details["route"] != "direct" {
		t.Errorf("route = %v, хотим direct", got.Details["route"])
	}
	if _, ok := got.Details["route_tunnel"]; ok {
		t.Errorf("route_tunnel при direct = %v: имени туннеля у прямого пути нет", got.Details["route_tunnel"])
	}
	got = base(func(context.Context, string) (wire.RouteLookupResult, error) {
		return wire.RouteLookupResult{}, errors.New("нет данных")
	}).Run(context.Background(), Deps{})
	if got.Details["route"] != "unknown" {
		t.Errorf("route при ошибке = %v, хотим unknown", got.Details["route"])
	}
	got = base(nil).Run(context.Background(), Deps{})
	if got.Details["route"] != "unknown" {
		t.Errorf("route без инструмента = %v, хотим unknown", got.Details["route"])
	}
}

// Экран обязан назвать последствие по имени: «через VPN-туннель «vpn-nl» --
// банки увидят не российский адрес». Без имени туннеля человеку нечего искать
// в своих правилах.
func TestDNSSplit_TunnelRouteCarriesTunnelName(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru"},
		YandexHost: testYandexHost,
		RouteLookup: func(context.Context, string) (wire.RouteLookupResult, error) {
			return wire.RouteLookupResult{Verdict: wire.LookupViaTunnel, TunnelID: "awg3", TunnelName: "vpn-nl"}, nil
		},
	}
	got := c.Run(context.Background(), Deps{})
	if got.Details["route"] != "tunnel" || got.Details["route_tunnel"] != "vpn-nl" {
		t.Errorf("route = %v, route_tunnel = %v; хотим tunnel и vpn-nl", got.Details["route"], got.Details["route_tunnel"])
	}
}

// Бэкенд пропускает в мини-апп только эти ключи (miniappCheckDetailsFrom в
// internal/backend/miniapp_check_facts.go). Новый или переименованный ключ
// без правки белого списка до экрана не доедет -- тогда поправь оба места.
func TestDNSSplit_DetailKeysMatchMiniappWhitelist(t *testing.T) {
	c := &DNSSplit{
		Zones:      []string{"ru"},
		YandexHost: testYandexHost,
		RouteLookup: func(context.Context, string) (wire.RouteLookupResult, error) {
			return wire.RouteLookupResult{Verdict: wire.LookupViaTunnel, TunnelID: "awg3", TunnelName: "vpn-nl"}, nil
		},
	}
	got := c.Run(context.Background(), Deps{})
	allowed := map[string]bool{"zones": true, "resolves": true, "route": true, "route_tunnel": true, "checked_at": true}
	for k := range got.Details {
		if !allowed[k] {
			t.Errorf("ключ %q не в белом списке мини-аппа", k)
		}
	}
	for k := range allowed {
		if _, ok := got.Details[k]; !ok {
			t.Errorf("белый список ждёт %q, агент его не шлёт", k)
		}
	}
}
