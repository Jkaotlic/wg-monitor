package checks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Проверка dns_split -- ЧИТАЮЩАЯ. Она не умеет FAIL вовсе: тревоги про DNS
// несут проверки dns и resolver_guard, и новая не имеет права ни разбудить
// человека ночью, ни попасть в счётчик тревог.
func TestDNSSplit_AllProbesFailStaysOKAndSaysUnknown(t *testing.T) {
	c := DNSSplit{
		Zones:         []string{"ru", "su"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		Resolve: func(context.Context, string, string) ([]string, error) {
			return nil, errors.New("сеть недоступна")
		},
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status = %q: эта проверка не умеет FAIL вовсе", got.Status)
	}
	zones, _ := got.Details["zones"].(map[string]string)
	if len(zones) != 2 {
		t.Fatalf("зон в ответе %d, хотим 2: %#v", len(zones), got.Details["zones"])
	}
	for z, verdict := range zones {
		if verdict != "unknown" {
			t.Errorf("зона %s: %q, хотим unknown", z, verdict)
		}
	}
}

// CDN отдаёт разным резолверам разные адреса: совпадение -- довод, а не
// доказательство. Слова уверенности в ответе быть не должно никогда.
func TestDNSSplit_NeverClaimsCertainty(t *testing.T) {
	c := DNSSplit{
		Zones:         []string{"ru"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		Resolve: func(_ context.Context, server, _ string) ([]string, error) {
			return []string{"198.51.100.7"}, nil
		},
	}
	b, _ := json.Marshal(c.Run(context.Background(), Deps{}).Details)
	for _, word := range []string{"точно", "гарант", "доказан"} {
		if strings.Contains(string(b), word) {
			t.Errorf("проверка заявила уверенность (%q): %s", word, b)
		}
	}
}

// Адреса локального dns-proxy совпали с Яндексом и разошлись с заграничным --
// «похоже, зона идёт через Яндекс». Это то свойство, ради которого оператор
// просил раздельный DNS: банки и госуслуги должны видеть российский адрес.
func TestDNSSplit_LocalMatchingYandexReadsAsYandex(t *testing.T) {
	c := DNSSplit{
		Zones:         []string{"ru"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		Resolve: func(_ context.Context, server, _ string) ([]string, error) {
			switch server {
			case "9.9.9.9":
				return []string{"203.0.113.9"}, nil
			default: // локальный и Яндекс отвечают одинаково
				return []string{"198.51.100.7"}, nil
			}
		},
	}
	zones, _ := c.Run(context.Background(), Deps{}).Details["zones"].(map[string]string)
	if zones["ru"] != "yandex" {
		t.Errorf("зона ru: %q, хотим yandex", zones["ru"])
	}
}

// Обратный случай: локальный резолвер отвечает как заграничный. Для ру-зоны это
// и есть поломка раздельной схемы, и сказать о ней надо прямо -- но без FAIL.
func TestDNSSplit_LocalMatchingForeignReadsAsForeign(t *testing.T) {
	c := DNSSplit{
		Zones:         []string{"ru"},
		DefaultCanary: "canary.example.com",
		YandexHost:    "common.dot.dns.yandex.net",
		Foreign:       []string{"9.9.9.9"},
		Resolve: func(_ context.Context, server, _ string) ([]string, error) {
			if server == "common.dot.dns.yandex.net" {
				return []string{"198.51.100.7"}, nil
			}
			return []string{"203.0.113.9"}, nil // локальный = заграничный
		},
	}
	got := c.Run(context.Background(), Deps{})
	if got.Status != "ok" {
		t.Fatalf("status = %q: даже поломка раздельной схемы не даёт FAIL", got.Status)
	}
	zones, _ := got.Details["zones"].(map[string]string)
	if zones["ru"] != "foreign" {
		t.Errorf("зона ru: %q, хотим foreign", zones["ru"])
	}
}

// Маршрут до самого резолвера Яндекса берётся у route_lookup -- второго такого
// инструмента писать нельзя. Его неудача -- «неизвестно», а не выдумка.
func TestDNSSplit_RouteComesFromRouteLookupAndDegradesToUnknown(t *testing.T) {
	base := func(rl func(context.Context, string) (string, error)) *DNSSplit {
		return &DNSSplit{
			Zones:         []string{"ru"},
			DefaultCanary: "canary.example.com",
			YandexHost:    "common.dot.dns.yandex.net",
			Foreign:       []string{"9.9.9.9"},
			Resolve: func(context.Context, string, string) ([]string, error) {
				return []string{"198.51.100.7"}, nil
			},
			RouteLookup: rl,
		}
	}
	got := base(func(context.Context, string) (string, error) { return "direct", nil }).Run(context.Background(), Deps{})
	if got.Details["route"] != "direct" {
		t.Errorf("route = %v, хотим direct", got.Details["route"])
	}
	got = base(func(context.Context, string) (string, error) { return "", errors.New("нет данных") }).Run(context.Background(), Deps{})
	if got.Details["route"] != "unknown" {
		t.Errorf("route при ошибке = %v, хотим unknown", got.Details["route"])
	}
	got = base(nil).Run(context.Background(), Deps{})
	if got.Details["route"] != "unknown" {
		t.Errorf("route без инструмента = %v, хотим unknown", got.Details["route"])
	}
}
