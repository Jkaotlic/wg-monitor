package main

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Сборка берёт всё из эталона: зоны — только те, у которых есть своя
// канарейка, и в порядке эталона; хост Яндекса и заграничные адреса — оттуда же.
func TestBuildDNSSplitCheck_TakesEverythingFromReference(t *testing.T) {
	c := buildDNSSplitCheck(awgmgr.New("http://127.0.0.1:1"))
	if c == nil {
		t.Fatal("проверка не собрана")
	}
	canaries := dnsref.ZoneCanaries()
	var want []string
	for _, z := range dnsref.RUZones() {
		if canaries[z] != "" {
			want = append(want, z)
		}
	}
	if !slices.Equal(c.Zones, want) {
		t.Errorf("зоны %v, хотим %v (только с канарейкой, порядок эталона)", c.Zones, want)
	}
	if c.YandexHost != dnsref.YandexDoTHost() {
		t.Errorf("хост Яндекса %q, хотим %q", c.YandexHost, dnsref.YandexDoTHost())
	}
	if !slices.Equal(c.Foreign, dnsref.ForeignResolverIPs()) {
		t.Errorf("заграничные %v, хотим %v", c.Foreign, dnsref.ForeignResolverIPs())
	}
	if c.DefaultCanary != "" {
		t.Errorf("DefaultCanary = %q: зона без своей канарейки получила бы вердикт соседней", c.DefaultCanary)
	}
	if c.MinInterval != 10*time.Minute {
		t.Errorf("MinInterval = %v, хотим 10m: агент отчитывается куда чаще", c.MinInterval)
	}
	if c.Resolve == nil || c.RouteLookup == nil {
		t.Error("Resolve или RouteLookup не проведены — проверка всегда отвечала бы «неизвестно»")
	}
}

// Переходник не заводит второй инструмент маршрута: он разбирает ответ
// существующего route_lookup, а его неудачу отдаёт ошибкой, не выдумкой.
func TestDNSSplitRouteLookup_ParsesRouteLookupAnswer(t *testing.T) {
	var asked string
	rl := dnsSplitRouteLookup(func(_ context.Context, host string) (string, error) {
		asked = host
		return `{"domain":"common.dot.dns.yandex.net","verdict":"tunnel","tunnel_id":"awg3","tunnel_name":"vpn-nl","by_default":true,"matches":[]}`, nil
	})
	got, err := rl(context.Background(), "common.dot.dns.yandex.net")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if asked != "common.dot.dns.yandex.net" {
		t.Errorf("спросили про %q", asked)
	}
	if got.Verdict != wire.LookupViaTunnel || got.TunnelName != "vpn-nl" {
		t.Errorf("разобрано %+v", got)
	}

	failing := dnsSplitRouteLookup(func(context.Context, string) (string, error) { return "", errors.New("awg-manager молчит") })
	if _, err := failing(context.Background(), "x.example.com"); err == nil {
		t.Error("ошибка route_lookup проглочена")
	}
	garbage := dnsSplitRouteLookup(func(context.Context, string) (string, error) { return "не json", nil })
	if _, err := garbage(context.Background(), "x.example.com"); err == nil {
		t.Error("мусор вместо ответа принят за ответ")
	}
}

// Адрес сервера для проб: у локального dns-proxy порт уже указан, у хоста
// Яндекса и заграничных IP — нет, IPv6 берётся в скобки.
func TestDNSServerAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:53":              "127.0.0.1:53",
		"9.9.9.9":                   "9.9.9.9:53",
		"common.dot.dns.yandex.net": "common.dot.dns.yandex.net:53",
		"2620:fe::fe":               "[2620:fe::fe]:53",
	}
	for in, want := range cases {
		if got := dnsServerAddr(in); got != want {
			t.Errorf("dnsServerAddr(%q) = %q, хотим %q", in, got, want)
		}
	}
}

// Проверка обязана попасть в отчёт агента. Без этого теста её регистрацию можно
// удалить бесследно: всё компилируется, все тесты зелёные, экран вечно говорит
// «появится после обновления агента».
func TestSingleChecks_IncludeDNSSplit(t *testing.T) {
	list := buildSingleChecks(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"), nil)
	for _, c := range list {
		if _, ok := c.(*checks.DNSSplit); ok {
			return
		}
	}
	names := make([]string, 0, len(list))
	for _, c := range list {
		names = append(names, c.Name())
	}
	t.Errorf("dns_split нет среди проверок отчёта (или она не по указателю — кеш не переживёт вызова): %v", names)
}
