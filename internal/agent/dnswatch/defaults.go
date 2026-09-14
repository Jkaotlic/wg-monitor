package dnswatch

import "github.com/Jkaotlic/wg-monitor/internal/agent/dnsref"

// Дефолты сторожа. Своей таблицы здесь больше нет -- она выводится из dnsref,
// единственного источника правды об эталонном раздельном DNS: русские зоны
// идут к Яндексу, остальное к заграничному пулу вперегонки, несколько CDN-зон
// закреплены за одним резолвером. Каждая строка -- команда dns-proxy без
// префикса, ровно как её добавляют через `ndmc -c "dns-proxy <строка>"`.
// Google отсутствует намеренно: он уводит CDN на американские узлы.
//
// Копия таблицы жила здесь и в actions/dns_reset.go, и они разошлись: сторож
// знал семь зон и не держал Google, ручной сброс -- три зоны и Google держал.
//
// agent.LoadConfig копирует это в конфиг агента, когда сторож включён, а
// список не задан. Значения -- СВОИ копии (dnsref отдаёт срезы копией),
// поэтому правка конфигом не уезжает в источник правды.
var (
	DefaultRUZones = dnsref.RUZones()

	DefaultRUCandidates = dnsref.RUCandidates()

	DefaultForeignCandidates = dnsref.ForeignCandidates()

	DefaultPinnedZones = dnsref.PinnedZones()
)

// DefaultPinnedCandidate несёт DefaultPinnedZones, пока жив. Переменная, а не
// константа: значение приходит из dnsref функцией.
var DefaultPinnedCandidate = dnsref.PinnedCandidate()
