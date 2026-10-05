package checks

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"golang.org/x/net/dns/dnsmessage"
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
		Resolve: func(_ context.Context, _ string, name string) ([]string, error) {
			if !routerResolves {
				return nil, errors.New("server misbehaving")
			}
			// Уникального имени нет -- честный ответ «такого имени нет».
			return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
		},
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

// Ревью, раунд 1: «роутер вообще резолвит» нельзя спрашивать именем, которое
// dns-proxy держит в кеше, -- все заграничные мертвы, а example.com отдаётся из
// кеша, и dns_ru поднимала бы вторую тревогу со словами «заграничные
// отвечают». Имя каждый раз новое; кешированный ответ на голое имя ничего не
// решает.
func TestDNSRu_CachedAnswerDoesNotCountAsResolving(t *testing.T) {
	p := &probeRecorder{dead: map[string]bool{testYandexHost: true, "9.9.9.9": true, "1.1.1.1": true}}
	c := newDNSRu(ruRouterConfig(), p, true)
	var asked []string
	c.Resolve = func(_ context.Context, server, name string) ([]string, error) {
		asked = append(asked, name)
		if server != "127.0.0.1:53" {
			t.Errorf("спросили %q, а не dns-proxy роутера", server)
		}
		if strings.TrimSuffix(name, ".") == "example.com" {
			return []string{"198.51.100.7"}, nil // из кеша
		}
		return nil, errors.New("server misbehaving") // свежего ответа нет
	}
	got := onlyCheck(t, c.Run(context.Background(), Deps{}))
	if got.Status == "fail" {
		t.Fatalf("dns-proxy ответил только из кеша, а dns_ru подняла тревогу: %+v", got.Details)
	}
	c.Run(context.Background(), Deps{})
	if len(asked) != 2 || asked[0] == asked[1] || !strings.HasSuffix(strings.TrimSuffix(asked[0], "."), ".example.com") {
		t.Errorf("имена проб «роутер резолвит»: %q -- хотим каждый раз новое под example.com", asked)
	}
}

// Сервер ответил «такого имени нет» или «записи нет» -- он работает: имя для
// пробы может и не существовать. Отказ (SERVFAIL, REFUSED) -- не работает.
func TestDNSRu_NameErrorMeansAnswered(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		fail bool
	}{
		{"нет имени", &DNSReplyError{Prefix: "dot: ", RCode: dnsmessage.RCodeNameError}, false},
		{"нет записи", &DNSReplyError{Prefix: "dot: ", NoAnswer: true}, false},
		{"отказ", &DNSReplyError{Prefix: "dot: ", RCode: dnsmessage.RCodeServerFailure}, true},
		{"молчит", errors.New("i/o timeout"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newDNSRu(ruRouterConfig(), &probeRecorder{}, true)
			c.Probe = func(context.Context, keenetic.DNSEndpoint, string) error { return tc.err }
			got := onlyCheck(t, c.Run(context.Background(), Deps{}))
			if (got.Status == "fail") != tc.fail {
				t.Errorf("status %q, хотим fail=%v", got.Status, tc.fail)
			}
		})
	}
}

// Имя пробы -- из зоны строки: сервер, которому отдана только su, спрашиваем
// именем в su, а не ya.ru.
func TestDNSRu_ProbeNameFromLineZone(t *testing.T) {
	p := &probeRecorder{}
	eps := []keenetic.DNSEndpoint{
		{Type: "dot", Host: "9.9.9.9", Port: 853},
		{Type: "dot", Host: "198.51.100.53", Port: 853, Zone: "su"},
		{Type: "dot", Host: "203.0.113.53", Port: 853, Zone: "xn--p1ai"},
		{Type: "dot", Host: testYandexHost, Port: 853, Zone: "ru"},
	}
	c := newDNSRu(eps, p, true)
	c.Run(context.Background(), Deps{})
	byHost := map[string]string{}
	for i, ep := range p.asked {
		byHost[ep.Host] = p.names[i]
	}
	if byHost[testYandexHost] != "ya.ru" {
		t.Errorf("ru: %q", byHost[testYandexHost])
	}
	for host, zone := range map[string]string{"198.51.100.53": "su", "203.0.113.53": "xn--p1ai"} {
		if n := byHost[host]; !strings.HasSuffix(n, "."+zone) {
			t.Errorf("сервер зоны %s спросили именем %q", zone, n)
		}
	}
}

// Проба с привязкой к интерфейсу собирается на каждый прогон (карта
// интерфейсов VPN-туннелей читается заново), как у проверки dns.
func TestDNSRu_PrepareProbePerRun(t *testing.T) {
	prepared := 0
	p := &probeRecorder{}
	c := newDNSRu(ruRouterConfig(), p, true)
	c.Probe = nil
	c.PrepareProbe = func(context.Context) func(context.Context, keenetic.DNSEndpoint, string) error {
		prepared++
		return p.probe
	}
	c.Run(context.Background(), Deps{})
	c.Run(context.Background(), Deps{})
	if prepared != 2 || len(p.asked) != 2 {
		t.Errorf("prepared=%d asked=%d, хотим 2/2", prepared, len(p.asked))
	}
}

// Ревью, раунд 1: агент перезапустился, первое чтение настроек не удалось --
// строка dns_ru не должна пропасть (бэкенд закрыл бы тревогу словами «в
// настройках больше нет…»). Знание «ру-апстримы были» живёт на диске.
func TestDNSRu_HadRUSurvivesRestart(t *testing.T) {
	path := t.TempDir() + "/dns-ru-state.json"
	first := newDNSRu(ruRouterConfig(), &probeRecorder{}, true)
	first.StatePath = path
	onlyCheck(t, first.Run(context.Background(), Deps{}))

	restarted := newDNSRu(nil, &probeRecorder{}, true)
	restarted.StatePath = path
	restarted.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) {
		return nil, errors.New("ndmc недоступен")
	}
	got := onlyCheck(t, restarted.Run(context.Background(), Deps{}))
	if got.Details["unverified"] != true {
		t.Fatalf("после перезапуска -- «не проверено», а не %+v", got)
	}

	// Ру-апстримы убрали -- на диске это тоже отражается.
	restarted.Endpoints = endpointsOf(keenetic.DNSEndpoint{Type: "dot", Host: "9.9.9.9", Port: 853})
	if got := restarted.Run(context.Background(), Deps{}); len(got) != 0 {
		t.Fatalf("ру-апстримов нет: %+v", got)
	}
	again := newDNSRu(nil, &probeRecorder{}, true)
	again.StatePath = path
	again.Endpoints = func(context.Context) ([]keenetic.DNSEndpoint, error) {
		return nil, errors.New("ndmc недоступен")
	}
	if got := again.Run(context.Background(), Deps{}); len(got) != 0 {
		t.Fatalf("ру-апстримов не было, а строка пришла: %+v", got)
	}
}

// Проба собрана как у dns: plain-апстрим, привязанный к интерфейсу
// VPN-туннеля, которого нет в свежей карте, пропускается -- и для dns_ru это
// «не проверено», а не «ру не отвечают».
func TestDNSRu_SkippedInterfaceIsNotFail(t *testing.T) {
	base := DNS{
		PerProbeTimeout:  100 * time.Millisecond,
		IfaceMapProvider: func(context.Context) (map[string]string, error) { return map[string]string{}, nil },
	}
	c := newDNSRu([]keenetic.DNSEndpoint{
		{Type: "plain", Host: "127.0.0.1", Port: 1, NDMSName: "Wireguard3", Zone: "ru"},
	}, &probeRecorder{}, true)
	c.Probe = nil
	c.PrepareProbe = base.PrepareProbe
	got := onlyCheck(t, c.Run(context.Background(), Deps{}))
	if got.Status == "fail" || got.Details["unverified"] != true {
		t.Fatalf("пропущенный апстрим стал провалом: %+v", got)
	}
}
