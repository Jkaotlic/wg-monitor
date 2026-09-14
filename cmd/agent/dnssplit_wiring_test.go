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
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Сборка берёт всё из эталона: все ру-зоны, хост Яндекса и канарейку живости.
func TestBuildDNSSplitCheck_TakesEverythingFromReference(t *testing.T) {
	c := buildDNSSplitCheck(awgmgr.New("http://127.0.0.1:1"))
	if c == nil {
		t.Fatal("проверка не собрана")
	}
	if !slices.Equal(c.Zones, dnsref.RUZones()) {
		t.Errorf("зоны %v, хотим %v", c.Zones, dnsref.RUZones())
	}
	if c.YandexHost != dnsref.YandexDoTHost() {
		t.Errorf("хост Яндекса %q, хотим %q", c.YandexHost, dnsref.YandexDoTHost())
	}
	if c.Canary != dnsref.RUCanary() {
		t.Errorf("канарейка %q, хотим %q", c.Canary, dnsref.RUCanary())
	}
	if c.MinInterval != 10*time.Minute {
		t.Errorf("MinInterval = %v, хотим 10m: агент отчитывается куда чаще", c.MinInterval)
	}
	if c.Endpoints == nil || c.Resolve == nil || c.RouteLookup == nil {
		t.Error("Endpoints, Resolve или RouteLookup не проведены — проверка всегда отвечала бы «неизвестно»")
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

// Адрес сервера для проб: у локального dns-proxy порт уже указан, у голого
// адреса — нет, IPv6 берётся в скобки.
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

// После настоящего сброса DNS раннер зовёт хук, и тот обязан отпустить кеш
// ИМЕННО той проверки, что стоит в отчёте, а не свежей копии.
func TestDNSChangedHook_InvalidatesReportedSplit(t *testing.T) {
	list := buildSingleChecks(&agent.Config{}, awgmgr.New("http://127.0.0.1:1"), nil)
	var split *checks.DNSSplit
	for _, c := range list {
		if s, ok := c.(*checks.DNSSplit); ok {
			split = s
		}
	}
	if split == nil {
		t.Fatal("dns_split нет в отчёте")
	}
	var reads int
	split.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) { reads++; return nil, nil }
	split.Resolve = func(context.Context, string, string) ([]string, error) { return []string{"198.51.100.7"}, nil }
	split.RouteLookup = func(context.Context, string) (wire.RouteLookupResult, error) {
		return wire.RouteLookupResult{Verdict: wire.LookupViaDirect}, nil
	}
	split.Run(context.Background(), checks.Deps{})
	hook := dnsChangedHook(list)
	if hook == nil {
		t.Fatal("хук не собран — сброс DNS не отпустит кеш")
	}
	hook()
	split.Run(context.Background(), checks.Deps{})
	if reads != 2 {
		t.Errorf("настройки прочитаны %d раз, ожидалось 2: хук не отпустил кеш проверки из отчёта", reads)
	}
}

// Раннер из сборки агента несёт все три защиты сброса DNS: путь конфига (рядом
// ляжет снимок «до»), свой резолвер оператора и хук сброса кеша.
func TestBuildRunner_WiresDNSResetGuards(t *testing.T) {
	cfg := &agent.Config{}
	cfg.DNSWatchdog.Endpoint = "https://dns.example.com/path"
	awg := awgmgr.New("http://127.0.0.1:1")
	list := buildSingleChecks(cfg, awg, nil)
	r := buildRunner(cfg, "/opt/etc/wg-monitor/config.yaml", awg, nil, nil, list)
	if r.ConfigPath != "/opt/etc/wg-monitor/config.yaml" {
		t.Errorf("ConfigPath = %q: снимок «до» не ляжет рядом с конфигом", r.ConfigPath)
	}
	if r.OwnResolverEndpoint != cfg.DNSWatchdog.Endpoint {
		t.Errorf("OwnResolverEndpoint = %q: сброс снёс бы свой резолвер", r.OwnResolverEndpoint)
	}
	if r.DNSChanged == nil {
		t.Error("DNSChanged не проведён: после сброса проверка раздельного DNS отвечала бы по старым настройкам")
	}
}
