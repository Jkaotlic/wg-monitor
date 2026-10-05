package checks

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DNSRuName -- имя проверки серверов имён для русских сайтов.
const DNSRuName = "dns_ru"

// DNSRu -- своя тревога, когда не отвечают серверы имён, которым роутер отдал
// русские зоны (спека v0.55, C; решение оператора 05.10.2026: отдельная
// тревога, даже когда заграничные живы).
//
// Зачем отдельно от dns: та считает различные апстримы равными, и сервер
// русских зон -- один голос из нескольких. Его падение кладёт ВСЕ русские
// сайты (банки, госуслуги), а порог dns при живых заграничных не набирается
// никогда.
//
// Правила:
//   - ру-апстрим -- строка настроек роутера, чья зона в эталоне несёт роль
//     ru (dnsref.ZonePurpose): кому роутер отдал русскую зону, тот и несёт
//     русские сайты;
//   - ру-апстримов в настройках нет -- проверки нет вовсе (Run отдаёт пустой
//     список, поэтому это MultiCheck);
//   - fail -- только когда ВСЕ ру-апстримы не ответили, а роутер при этом
//     вообще резолвит (заграничное имя через свой dns-proxy). Не резолвит --
//     это общая беда проверки dns, вторая тревога не нужна: «не проверено»;
//   - проба, не успевшая из-за бюджета отчёта, -- не провал.
//
// Порог тревоги -- общий автомат бэкенда, своих правил у проверки нет.
type DNSRu struct {
	// Endpoints читает апстримы dns-proxy из настроек роутера.
	Endpoints func(ctx context.Context) ([]keenetic.DNSEndpoint, error)
	// Probe спрашивает имя у самого апстрима (его транспортом), nil -- ответил.
	Probe func(ctx context.Context, ep keenetic.DNSEndpoint, name string) error
	// Resolve спрашивает имя у конкретного сервера (здесь -- у dns-proxy
	// роутера).
	Resolve func(ctx context.Context, server, name string) ([]string, error)
	// RUName -- имя в русской зоне, которым пробуем ру-апстримы.
	RUName string
	// ForeignName -- заграничное имя: «роутер вообще резолвит».
	ForeignName string

	PerProbeTimeout time.Duration
	// ConfigInterval -- как долго верить прочитанным настройкам. Чтение
	// running-config через ndmc не бесплатное, а настройки меняются редко;
	// пробы при этом идут каждый отчёт. 0 -- читать каждый раз.
	ConfigInterval time.Duration
	Now            func() time.Time

	mu        sync.Mutex
	cfg       []keenetic.DNSEndpoint
	cfgAt     time.Time
	cfgCached bool
	// hadRU -- в последних прочитанных настройках ру-апстримы были. Нужен,
	// когда настройки не прочитались: тогда «не проверено» уместно только
	// там, где проверка вообще существует.
	hadRU bool
}

func (c *DNSRu) Group() string { return DNSRuName }

// Invalidate отпускает прочитанные настройки: после сброса DNS следующий
// отчёт обязан смотреть на новые.
func (c *DNSRu) Invalidate() {
	c.mu.Lock()
	c.cfgCached = false
	c.cfg = nil
	c.mu.Unlock()
}

func (c *DNSRu) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *DNSRu) probeTimeout() time.Duration {
	if c.PerProbeTimeout > 0 {
		return c.PerProbeTimeout
	}
	return 3 * time.Second
}

// settings -- настройки роутера из кеша или свежим чтением.
func (c *DNSRu) settings(ctx context.Context) ([]keenetic.DNSEndpoint, error) {
	now := c.now()
	c.mu.Lock()
	if c.cfgCached && c.ConfigInterval > 0 && now.Sub(c.cfgAt) < c.ConfigInterval {
		eps := c.cfg
		c.mu.Unlock()
		return eps, nil
	}
	c.mu.Unlock()
	if c.Endpoints == nil {
		return nil, fmt.Errorf("dns settings reader is not wired")
	}
	eps, err := c.Endpoints(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cfg, c.cfgAt, c.cfgCached = eps, now, true
	c.mu.Unlock()
	return eps, nil
}

// ruUpstreams -- различные ру-апстримы настроек: строки зон одного сервера
// (`tls upstream <host> domain ru|su|…`) -- одна проба.
func ruUpstreams(eps []keenetic.DNSEndpoint) []keenetic.DNSEndpoint {
	var ru []keenetic.DNSEndpoint
	for _, ep := range eps {
		if ep.Zone != "" && dnsref.ZonePurpose(ep.Zone) == dnsref.PurposeRU {
			ru = append(ru, ep)
		}
	}
	return dedupEndpoints(ru)
}

func (c *DNSRu) Run(ctx context.Context, _ Deps) []wire.Check {
	start := time.Now()
	eps, err := c.settings(ctx)
	if err != nil {
		c.mu.Lock()
		hadRU := c.hadRU
		c.mu.Unlock()
		if !hadRU {
			return nil
		}
		return []wire.Check{Unverified(DNSRuName, start, "dns settings could not be read",
			map[string]any{"discovery_error": err.Error()})}
	}
	ru := ruUpstreams(eps)
	c.mu.Lock()
	c.hadRU = len(ru) > 0
	c.mu.Unlock()
	if len(ru) == 0 {
		return nil
	}

	type epResult struct {
		Type         string `json:"type"`
		Target       string `json:"target"`
		Reachable    bool   `json:"reachable"`
		Inconclusive bool   `json:"inconclusive,omitempty"`
		Err          string `json:"err,omitempty"`
	}
	results := make([]epResult, len(ru))
	var routerResolves bool
	var wg sync.WaitGroup
	for i, ep := range ru {
		wg.Add(1)
		go func(i int, ep keenetic.DNSEndpoint) {
			defer wg.Done()
			r := epResult{Type: ep.Type, Target: epTarget(ep)}
			pctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
			defer cancel()
			var perr error
			if c.Probe == nil {
				perr = fmt.Errorf("probe is not wired")
			} else {
				perr = c.Probe(pctx, ep, c.RUName)
			}
			switch {
			case perr == nil:
				r.Reachable = true
			case probeInconclusive(ctx, perr):
				r.Inconclusive = true
				r.Err = perr.Error()
			default:
				r.Err = perr.Error()
			}
			results[i] = r
		}(i, ep)
	}
	// «Роутер вообще резолвит» спрашиваем одновременно с пробами: бюджет
	// отчёта на проверку общий, а запрос к своему dns-proxy дешёвый.
	wg.Add(1)
	go func() {
		defer wg.Done()
		routerResolves = c.routerResolves(ctx)
	}()
	wg.Wait()

	failed, inconclusive := 0, 0
	for _, r := range results {
		switch {
		case r.Inconclusive:
			inconclusive++
		case !r.Reachable:
			failed++
		}
	}
	details := map[string]any{
		"ru_upstreams":     len(ru),
		"ru_failed":        failed,
		"router_resolves":  routerResolves,
		"endpoints_detail": results,
	}
	switch {
	case len(ru)-failed-inconclusive > 0:
		// Хотя бы один ру-апстрим ответил.
		return []wire.Check{OK(DNSRuName, start, details)}
	case inconclusive > 0:
		return []wire.Check{Unverified(DNSRuName, start, "check ran out of time before every ru upstream answered", details)}
	case !routerResolves:
		return []wire.Check{Unverified(DNSRuName, start, "router does not resolve at all: that is the dns check's concern", details)}
	}
	return []wire.Check{Fail(DNSRuName, start,
		fmt.Sprintf("all %d ru upstreams unreachable", len(ru)), details)}
}

// routerResolves -- отвечает ли dns-proxy роутера на заграничное имя.
func (c *DNSRu) routerResolves(ctx context.Context) bool {
	if c.Resolve == nil || c.ForeignName == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	addrs, err := c.Resolve(ctx, localResolver, c.ForeignName)
	return err == nil && len(addrs) > 0
}
