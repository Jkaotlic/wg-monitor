package checks

import (
	"context"
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
	Now             func() time.Time
}

func (DNSSplit) Name() string { return "dns_split" }

// localResolver -- dns-proxy самого роутера: именно его ответы и означают, что
// увидит человек за этим роутером.
const localResolver = "127.0.0.1:53"

func (c DNSSplit) Run(ctx context.Context, _ Deps) wire.Check {
	start := time.Now()
	if c.PerProbeTimeout <= 0 {
		c.PerProbeTimeout = 2 * time.Second
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}

	zones := make(map[string]string, len(c.Zones))
	for _, z := range c.Zones {
		zones[z] = c.zoneVerdict(ctx, z)
	}

	details := map[string]any{
		"zones":      zones,
		"route":      c.routeVerdict(ctx),
		"checked_at": now().UTC().Format(time.RFC3339),
	}
	return OK(c.Name(), start, details)
}

// zoneVerdict сравнивает ответ локального dns-proxy с ответами Яндекса и
// заграничного резолвера. Любая неудача пробы -- «неизвестно»: выдумывать
// вердикт по неполным данным хуже, чем честно сказать, что не знаем.
func (c DNSSplit) zoneVerdict(ctx context.Context, zone string) string {
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
func (c DNSSplit) routeVerdict(ctx context.Context) string {
	if c.RouteLookup == nil || c.YandexHost == "" {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, c.PerProbeTimeout)
	defer cancel()
	verdict, err := c.RouteLookup(ctx, c.YandexHost)
	if err != nil || verdict == "" {
		return "unknown"
	}
	return verdict
}

func (c DNSSplit) probe(ctx context.Context, server, name string) []string {
	if server == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.PerProbeTimeout)
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
