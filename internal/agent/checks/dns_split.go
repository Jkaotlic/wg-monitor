package checks

import (
	"context"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// DNSSplit -- читающая проверка раздельного DNS: идут ли русские зоны к
// Яндексу, а остальное мимо него.
//
// Она НЕ УМЕЕТ FAIL вовсе, и это главное её свойство. Тревоги про DNS уже несут
// проверки dns и resolver_guard; новая не имеет права ни разбудить человека
// ночью, ни попасть в счётчик тревог. Её дело -- рассказать, а не поднять
// тревогу, поэтому вердикт лежит в details, а статус всегда ok.
//
// Второе свойство -- честность формулировок. Совпадение адресов это ДОВОД, а не
// доказательство: CDN отдаёт разным резолверам разные адреса, и на полностью
// здоровой схеме ответы могут разойтись. Поэтому вердикты называются
// «yandex» / «foreign» / «unknown», а слова уверенности («точно», «гарантирую»,
// «доказано») в ответе не появляются никогда — экран обязан говорить «похоже».
//
// Третье -- бюджет. У каждой проверки в отчёте агента жёсткий лимит времени, а
// семь зон по три пробы это 21 обращение: подряд они в лимит не влезут. Поэтому
// вердикт считается не чаще раза в MinInterval, а между пересчётами отдаётся из
// кеша. Проверку держат по указателю: на копии структуры кеш не пережил бы
// вызова.
type DNSSplit struct {
	// Zones -- какие зоны проверяем (обычно dnsref.RUZones()).
	Zones []string
	// ZoneCanaries -- имя-канарейка на зону. Одной канарейки на семь зон не
	// хватает: через неё нечем проверить ни tatar, ни xn--d1acj3b.
	ZoneCanaries map[string]string
	// DefaultCanary -- чем спрашивать зону, для которой своей канарейки нет.
	DefaultCanary string
	// YandexHost -- DoT-хост Яндекса (dnsref.YandexDoTHost()).
	YandexHost string
	// Foreign -- заграничные резолверы; спрашиваем первый доступный.
	Foreign []string
	// Resolve спрашивает имя у конкретного сервера. Внедряется, чтобы проверку
	// можно было прогнать без сети.
	Resolve func(ctx context.Context, server, name string) ([]string, error)
	// RouteLookup -- «куда пойдёт хост»: берётся у actions.RouteLookup, второго
	// такого инструмента писать нельзя. nil или ошибка -> «неизвестно».
	RouteLookup func(ctx context.Context, host string) (string, error)

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

	zones := make(map[string]string, len(c.Zones))
	anyUnknown := false
	for _, z := range c.Zones {
		v := c.zoneVerdict(ctx, z)
		if v == "unknown" {
			anyUnknown = true
		}
		zones[z] = v
	}
	route := c.routeVerdict(ctx)
	if route == "unknown" {
		anyUnknown = true
	}

	details := map[string]any{
		"zones":      zones,
		"route":      route,
		"checked_at": now.UTC().Format(time.RFC3339),
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

// zoneVerdict сравнивает ответ локального dns-proxy с ответами Яндекса и
// заграничного резолвера. Любая неудача пробы -- «неизвестно»: выдумывать
// вердикт по неполным данным хуже, чем честно сказать, что не знаем.
func (c *DNSSplit) zoneVerdict(ctx context.Context, zone string) string {
	canary := c.DefaultCanary
	if v, ok := c.ZoneCanaries[zone]; ok && v != "" {
		canary = v
	}
	if canary == "" || c.Resolve == nil {
		return "unknown"
	}

	local := c.probe(ctx, localResolver, canary)
	if len(local) == 0 {
		return "unknown"
	}
	yandex := c.probe(ctx, c.YandexHost, canary)
	var foreign []string
	for _, f := range c.Foreign {
		if foreign = c.probe(ctx, f, canary); len(foreign) > 0 {
			break
		}
	}
	if len(yandex) == 0 || len(foreign) == 0 {
		return "unknown"
	}

	sameAsYandex := intersects(local, yandex)
	sameAsForeign := intersects(local, foreign)
	switch {
	case sameAsYandex && !sameAsForeign:
		return "yandex"
	case sameAsForeign && !sameAsYandex:
		return "foreign"
	default:
		// Совпало с обоими или ни с одним: CDN вправе так ответить, и это не
		// повод что-то утверждать.
		return "unknown"
	}
}

// routeVerdict спрашивает, как идёт трафик до самого резолвера Яндекса: мимо
// туннеля или через него. Свойство «мимо VPN» -- половина требования оператора,
// вторая половина (транспорт DoT) видна проверке dns.
func (c *DNSSplit) routeVerdict(ctx context.Context) string {
	if c.RouteLookup == nil || c.YandexHost == "" {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	verdict, err := c.RouteLookup(ctx, c.YandexHost)
	if err != nil || verdict == "" {
		return "unknown"
	}
	return verdict
}

func (c *DNSSplit) probe(ctx context.Context, server, name string) []string {
	if server == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.probeTimeout())
	defer cancel()
	addrs, err := c.Resolve(ctx, server, name)
	if err != nil {
		return nil
	}
	return addrs
}

func intersects(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if set[v] {
			return true
		}
	}
	return false
}
