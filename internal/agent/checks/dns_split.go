package checks

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DNSSplit -- читающая проверка раздельного DNS: кому роутер отдал русские
// зоны, каким транспортом, и идёт ли запрос к Яндексу мимо VPN-туннеля.
//
// Она НЕ УМЕЕТ FAIL вовсе, и это главное её свойство. Тревоги про DNS уже несут
// проверки dns и resolver_guard; новая не имеет права ни разбудить человека
// ночью, ни попасть в счётчик тревог. Её дело -- рассказать, а не поднять
// тревогу, поэтому вердикт лежит в details, а статус всегда ok.
//
// Второе свойство -- честность источника. Зонный вердикт берётся из НАСТРОЕК
// роутера (running-config), а не из сравнения ответов резолверов: живой прогон
// 14.09.2026 показал, что Яндекс, Quad9 и Cloudflare отдают одинаковые адреса на
// все крупные русские сайты, и сравнение ничего не различает. Поэтому экран
// говорит «по настройкам роутера», а слова уверенности («точно», «гарантирую»,
// «доказано») в ответе не появляются никогда. Настройки дополняет одна проба:
// резолвит ли роутер вообще.
//
// Третье -- бюджет. Чтение running-config через ndmc не бесплатное, а агент
// отчитывается куда чаще, чем меняются настройки. Поэтому вердикт считается не
// чаще раза в MinInterval, а между пересчётами отдаётся из кеша. Проверку держат
// по указателю: на копии структуры кеш не пережил бы вызова.
type DNSSplit struct {
	// Zones -- какие зоны проверяем (обычно dnsref.RUZones()).
	Zones []string
	// YandexHost -- DoT-хост Яндекса (dnsref.YandexDoTHost()).
	YandexHost string
	// Canary -- имя, которым проверяем, что dns-proxy роутера вообще отвечает.
	Canary string
	// Endpoints читает апстримы dns-proxy из настроек роутера.
	Endpoints func(ctx context.Context) ([]keenetic.DNSEndpoint, error)
	// Resolve спрашивает имя у конкретного сервера. Внедряется, чтобы проверку
	// можно было прогнать без сети.
	Resolve func(ctx context.Context, server, name string) ([]string, error)
	// RouteLookup -- «куда пойдёт хост»: берётся у actions.RouteLookup, второго
	// такого инструмента писать нельзя. nil или ошибка -> «неизвестно». Ответ
	// целиком, а не один вердикт: экрану нужно имя туннеля, чтобы назвать
	// последствие.
	RouteLookup func(ctx context.Context, host string) (wire.RouteLookupResult, error)

	PerProbeTimeout time.Duration
	// MinInterval -- как часто пересчитывать вердикт. 0 -- считать каждый раз
	// (так удобно тестам, в бою значение задаёт сборка агента).
	MinInterval time.Duration
	Now         func() time.Time

	mu        sync.Mutex
	cached    map[string]any
	cachedAt  time.Time
	cacheHeld time.Duration
}

// Вердикты зоны. Различаются, потому что человеку чинить разное.
const (
	zoneYandexDoT = "yandex_dot" // Яндекс по DoT -- как задумано
	zoneYandexDoH = "yandex_doh" // Яндекс, но транспорт не DoT
	zoneMixed     = "mixed"      // поделена между Яндексом и чужим резолвером
	zoneOther     = "other"      // отдана чужому резолверу
	zoneNone      = "none"       // отдельного правила нет -- уходит на общие серверы
	zoneUnknown   = "unknown"    // настройки не прочитались
)

func (c *DNSSplit) Name() string { return "dns_split" }

// localResolver -- dns-proxy самого роутера: именно его ответы и означают, что
// увидит человек за этим роутером.
const localResolver = "127.0.0.1:53"

// unknownRetryDivisor -- во сколько раз короче живёт неудачный вердикт. Без
// этого одна неудачная проба заморозила бы «неизвестно» на весь MinInterval, и
// экран говорил бы «не знаю» уже после того, как ответ появился.
const unknownRetryDivisor = 10

func (c *DNSSplit) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	now := c.now()

	c.mu.Lock()
	if c.cached != nil && c.cacheHeld > 0 && now.Sub(c.cachedAt) < c.cacheHeld {
		details := c.cached
		c.mu.Unlock()
		return OK(c.Name(), start, details)
	}
	c.mu.Unlock()

	anyUnknown := false
	zones := c.zoneVerdicts(ctx)
	for _, v := range zones {
		if v == zoneUnknown {
			anyUnknown = true
		}
	}
	resolves := c.resolveProbe(ctx)
	if resolves != "ok" {
		anyUnknown = true
	}
	route, tunnel := c.routeVerdict(ctx)
	if route == "unknown" {
		anyUnknown = true
	}

	details := map[string]any{
		"zones":      zones,
		"resolves":   resolves,
		"route":      route,
		"checked_at": now.UTC().Format(time.RFC3339),
	}
	if route == wire.LookupViaTunnel && tunnel != "" {
		details["route_tunnel"] = tunnel
	}

	hold := c.MinInterval
	if anyUnknown && hold > 0 {
		hold /= unknownRetryDivisor
	}
	c.mu.Lock()
	c.cached, c.cachedAt, c.cacheHeld = details, now, hold
	c.mu.Unlock()

	return OK(c.Name(), start, details)
}

func (c *DNSSplit) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *DNSSplit) probeTimeout() time.Duration {
	if c.PerProbeTimeout > 0 {
		return c.PerProbeTimeout
	}
	return 2 * time.Second
}

// zoneVerdicts читает настройки один раз на все зоны. Не прочиталось --
// «неизвестно» для каждой: выдумывать вердикт хуже, чем честно не знать.
func (c *DNSSplit) zoneVerdicts(ctx context.Context) map[string]string {
	out := make(map[string]string, len(c.Zones))
	var eps []keenetic.DNSEndpoint
	ok := false
	if c.Endpoints != nil {
		var err error
		eps, err = c.Endpoints(ctx)
		ok = err == nil
	}
	for _, z := range c.Zones {
		if !ok {
			out[z] = zoneUnknown
			continue
		}
		out[z] = c.zoneVerdict(splitNormHost(z), eps)
	}
	return out
}

// zoneVerdict сопоставляет строки по смыслу -- хост, зона, транспорт, -- а не
// по написанию: роутер вправе записать строку в своей форме, а Яндекс -- адресом
// с именем сертификата (sni).
func (c *DNSSplit) zoneVerdict(zone string, eps []keenetic.DNSEndpoint) string {
	yandex := splitNormHost(c.YandexHost)
	var yDoT, yDoH, other bool
	for _, ep := range eps {
		if ep.Zone == "" || splitNormHost(ep.Zone) != zone {
			continue
		}
		switch {
		case ep.Type == "dot" && (splitNormHost(ep.Host) == yandex || splitNormHost(ep.SNI) == yandex):
			yDoT = true
		case ep.Type == "doh" && splitDoHHost(ep.URL) == yandex:
			yDoH = true
		default:
			other = true
		}
	}
	switch {
	case other && (yDoT || yDoH):
		return zoneMixed
	case other:
		return zoneOther
	case yDoT:
		return zoneYandexDoT
	case yDoH:
		return zoneYandexDoH
	default:
		return zoneNone
	}
}

// resolveProbe -- резолвит ли dns-proxy роутера вообще: настройки без живого
// ответа ничего не стоят.
func (c *DNSSplit) resolveProbe(ctx context.Context) string {
	if c.Resolve == nil || c.Canary == "" {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	addrs, err := c.Resolve(ctx, localResolver, c.Canary)
	if err != nil || len(addrs) == 0 {
		return "fail"
	}
	return "ok"
}

// routeVerdict спрашивает, как идёт трафик до самого резолвера Яндекса: мимо
// туннеля или через него. Свойство «мимо VPN» -- половина требования оператора,
// вторая половина (транспорт DoT) видна по настройкам. Второе значение -- имя
// туннеля, когда путь идёт через него.
func (c *DNSSplit) routeVerdict(ctx context.Context) (verdict, tunnel string) {
	if c.RouteLookup == nil || c.YandexHost == "" {
		return "unknown", ""
	}
	ctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	res, err := c.RouteLookup(ctx, c.YandexHost)
	if err != nil || res.Verdict == "" {
		return "unknown", ""
	}
	name := res.TunnelName
	if name == "" {
		name = res.TunnelID
	}
	return res.Verdict, name
}

func splitNormHost(s string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(s)), ".")
}

func splitDoHHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return splitNormHost(u.Hostname())
}
