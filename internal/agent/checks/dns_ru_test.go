package checks

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Настройки роутера: два заграничных апстрима и Яндекс на русских зонах --
// строкой на зону, как пишет эталон.
func ruRouterConfig() []keenetic.DNSEndpoint {
	return []keenetic.DNSEndpoint{
		{Type: "dot", Host: "9.9.9.9", Port: 853, SNI: "dns.quad9.net"},
		{Type: "dot", Host: "1.1.1.1", Port: 853, SNI: "cloudflare-dns.com"},
		{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"},
		{Type: "dot", Host: testYandexHost, Port: 853, Zone: "su"},
		{Type: "dot", Host: testYandexHost, Port: 853, Zone: "xn--p1ai"},
	}
}

// probeDeadHosts -- апстримы с этими хостами не отвечают, остальные живы.
// Заодно запоминает, кого спрашивали.
type probeRecorder struct {
	mu    sync.Mutex
	dead  map[string]bool
	asked []keenetic.DNSEndpoint
	names []string
}

func (p *probeRecorder) probe(_ context.Context, ep keenetic.DNSEndpoint, name string) error {
	p.mu.Lock()
	p.asked = append(p.asked, ep)
	p.names = append(p.names, name)
	p.mu.Unlock()
	if p.dead[ep.Host] {
		return errors.New("i/o timeout")
	}
	return nil
}

func newDNSRu(eps []keenetic.DNSEndpoint, p *probeRecorder, routerResolves bool) *DNSRu {
	return &DNSRu{
		Endpoints: endpointsOf(eps...),
		Probe:     p.probe,
		Resolve: func(context.Context, string, string) ([]string, error) {
			if !routerResolves {
				return nil, errors.New("no answer")
			}
			return []string{"198.51.100.7"}, nil
		},
		RUName:      "ya.ru",
		ForeignName: "example.com",
	}
}

func onlyCheck(t *testing.T, got []wire.Check) wire.Check {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("строк проверки %d, хотим 1: %+v", len(got), got)
	}
	if got[0].Name != "dns_ru" {
		t.Fatalf("имя проверки %q, хотим dns_ru", got[0].Name)
	}
	return got[0]
}

// Главный случай спеки C: все ру-апстримы молчат, а роутер при этом
// резолвит (заграничные живы) -- своя тревога. Проверка dns тут молчит:
// один мёртвый из трёх различных не набирает её порога.
func TestDNSRu_AllRUDownRouterResolves_Fails(t *testing.T) {
	p := &probeRecorder{dead: map[string]bool{testYandexHost: true}}
	got := onlyCheck(t, newDNSRu(ruRouterConfig(), p, true).Run(context.Background(), Deps{}))
	if got.Status != "fail" {
		t.Fatalf("status = %q, хотим fail: %+v", got.Status, got.Details)
	}
	if got.Details["ru_upstreams"] != 1 || got.Details["ru_failed"] != 1 {
		t.Errorf("ru_upstreams/ru_failed = %v/%v, хотим 1/1 (Яндекс один, строк зон три)",
			got.Details["ru_upstreams"], got.Details["ru_failed"])
	}
	if got.Details["router_resolves"] != true {
		t.Errorf("router_resolves = %v", got.Details["router_resolves"])
	}
	// Ру-апстрим спрашиваем русским именем, и только ру-апстримы: заграничные
	// -- забота проверки dns.
	for i, ep := range p.asked {
		if ep.Host != testYandexHost {
			t.Errorf("спросили заграничный апстрим %q", ep.Host)
		}
		if p.names[i] != "ya.ru" {
			t.Errorf("ру-апстрим спросили про %q, хотим ya.ru", p.names[i])
		}
	}
	if len(p.asked) != 1 {
		t.Errorf("проб %d, хотим 1: строки зон одного сервера -- одна проба", len(p.asked))
	}
}

// Роутер не резолвит вообще -- это общая беда проверки dns, вторая тревога не
// нужна. Но и «ok» неправда: проверка ничего не установила.
func TestDNSRu_RouterDoesNotResolveAtAll_NotFail(t *testing.T) {
	p := &probeRecorder{dead: map[string]bool{testYandexHost: true, "9.9.9.9": true, "1.1.1.1": true}}
	got := onlyCheck(t, newDNSRu(ruRouterConfig(), p, false).Run(context.Background(), Deps{}))
	if got.Status != "ok" {
		t.Fatalf("status = %q, хотим ok (unverified): общая беда -- у проверки dns", got.Status)
	}
	if got.Details["unverified"] != true {
		t.Errorf("нет пометки unverified: %+v", got.Details)
	}
	if got.Details["router_resolves"] != false {
		t.Errorf("router_resolves = %v, хотим false", got.Details["router_resolves"])
	}
}

// Ру-апстримов в настройках нет -- проверки нет вовсе: ни строки, ни «ok».
func TestDNSRu_NoRUUpstreams_NoCheck(t *testing.T) {
	p := &probeRecorder{}
	eps := []keenetic.DNSEndpoint{
		{Type: "dot", Host: "9.9.9.9", Port: 853, SNI: "dns.quad9.net"},
		{Type: "doh", URL: "https://cloudflare-dns.com/dns-query", Zone: "tmdb.org"},
	}
	got := newDNSRu(eps, p, true).Run(context.Background(), Deps{})
	if len(got) != 0 {
		t.Fatalf("ру-апстримов нет, а проверка пришла: %+v", got)
	}
	if len(p.asked) != 0 {
		t.Errorf("пробовали апстримы без ру-зон: %+v", p.asked)
	}
}

// Хотя бы один ру-апстрим отвечает -- русские сайты открываются.
func TestDNSRu_OneRUAlive_OK(t *testing.T) {
	eps := append(ruRouterConfig(), keenetic.DNSEndpoint{Type: "doh", URL: "https://dns.example.net/dns-query", Zone: "ru"})
	p := &probeRecorder{dead: map[string]bool{testYandexHost: true}}
	got := onlyCheck(t, newDNSRu(eps, p, true).Run(context.Background(), Deps{}))
	if got.Status != "ok" || got.Details["unverified"] == true {
		t.Fatalf("status = %q details=%+v, хотим чистый ok", got.Status, got.Details)
	}
	if got.Details["ru_upstreams"] != 2 || got.Details["ru_failed"] != 1 {
		t.Errorf("ru_upstreams/ru_failed = %v/%v", got.Details["ru_upstreams"], got.Details["ru_failed"])
	}
}

// Настройки не прочитались: если ру-апстримы раньше были -- «не проверено»;
// если о них ничего не известно -- строки нет (иначе на роутерах без ру-зон
// появлялась бы строка, которой там быть не должно).
func TestDNSRu_SettingsUnreadable(t *testing.T) {
	fail := false
	p := &probeRecorder{}
	c := newDNSRu(nil, p, true)
	c.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) {
		if fail {
			return nil, errors.New("ndmc недоступен")
		}
		return ruRouterConfig(), nil
	}
	fail = true
	if got := c.Run(context.Background(), Deps{}); len(got) != 0 {
		t.Fatalf("о ру-апстримах ничего не известно, а строка пришла: %+v", got)
	}
	fail = false
	if got := onlyCheck(t, c.Run(context.Background(), Deps{})); got.Status != "ok" {
		t.Fatalf("живой Яндекс: %+v", got)
	}
	fail = true
	got := onlyCheck(t, c.Run(context.Background(), Deps{}))
	if got.Status != "ok" || got.Details["unverified"] != true {
		t.Fatalf("настройки не прочитались -- «не проверено», а не %+v", got)
	}
}

// Проверка не успела (бюджет отчёта кончился) -- это не «ру не отвечают».
func TestDNSRu_CanceledIsNotFail(t *testing.T) {
	p := &probeRecorder{dead: map[string]bool{testYandexHost: true}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := onlyCheck(t, newDNSRu(ruRouterConfig(), p, true).Run(ctx, Deps{}))
	if got.Status == "fail" {
		t.Fatalf("отменённая проверка подняла тревогу: %+v", got)
	}
}

// Настройки читаются не на каждый отчёт: ndmc не бесплатный. Пробы -- каждый
// раз. Invalidate (после сброса DNS) заставляет перечитать.
func TestDNSRu_SettingsCachedBetweenRuns(t *testing.T) {
	reads := 0
	p := &probeRecorder{}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	c := newDNSRu(nil, p, true)
	c.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) {
		reads++
		return ruRouterConfig(), nil
	}
	c.ConfigInterval = 10 * time.Minute
	c.Now = func() time.Time { return now }
	c.Run(context.Background(), Deps{})
	now = now.Add(time.Minute)
	c.Run(context.Background(), Deps{})
	if reads != 1 {
		t.Errorf("настройки прочитаны %d раз за минуту, хотим 1", reads)
	}
	if len(p.asked) != 2 {
		t.Errorf("проб %d, хотим 2: пробы идут каждый отчёт", len(p.asked))
	}
	c.Invalidate()
	c.Run(context.Background(), Deps{})
	if reads != 2 {
		t.Errorf("после Invalidate настройки не перечитаны: %d", reads)
	}
}

// Боевая проба dns_ru -- та же, что у dns: живой апстрим отвечает, мёртвый нет.
func TestDNSProbeEndpoint_SameTransportAsDNS(t *testing.T) {
	server, stop := startMockUDPDNS(t, [4]byte{198, 51, 100, 7})
	defer stop()
	host, port := splitHostPort(t, server)
	probe := DNS{PerProbeTimeout: 200 * time.Millisecond}.ProbeEndpoint
	if err := probe(context.Background(), keenetic.DNSEndpoint{Type: "plain", Host: host, Port: port}, "ya.ru"); err != nil {
		t.Fatalf("живой апстрим: %v", err)
	}
	if err := probe(context.Background(), keenetic.DNSEndpoint{Type: "plain", Host: "127.0.0.1", Port: 1}, "ya.ru"); err == nil {
		t.Fatal("мёртвый апстрим ответил")
	}
}
