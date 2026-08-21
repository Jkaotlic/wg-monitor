// internal/backend/heartbeat/stats.go
package heartbeat

import "time"

// Stats -- снимок счётчиков сторожа для операторской панели.
//
// Сторож молчит в двух случаях: когда в парке всё хорошо и когда он сам мёртв.
// Счётчики эту разницу видят, но лежат в /debug/vars, который слушает только
// loopback, -- то есть прочитать их можно лишь с самой машины, по ssh. Ровно
// тогда, когда это нужнее всего (парк молчит, а тревог нет), ssh обычно и
// недоступен. Поэтому те же числа отдаются в сводку панели.
type Stats struct {
	ScansTotal    int64     `json:"scans_total"`
	LastScanAt    time.Time `json:"last_scan_at"`
	LastScanMs    int64     `json:"last_scan_ms"`
	StaleUsers    int64     `json:"stale_users"`
	OfflineSent   int64     `json:"offline_sent_total"`
	OfflineErrors int64     `json:"offline_errors_total"`
	SleepSent     int64     `json:"sleep_sent_total"`
	// ScanEvery -- настроенный шаг обхода. Без него «последний обход 40
	// секунд назад» ничего не значит: это норма при шаге в минуту и тревога
	// при шаге в пять секунд.
	ScanEvery time.Duration `json:"scan_every_sec"`
}

// Snapshot читает счётчики процесса. Метод на Watcher, а не пакетная функция:
// шаг обхода знает только он.
func (w *Watcher) Snapshot() Stats {
	var last time.Time
	if unix := metricLastScanUnix.Value(); unix > 0 {
		last = time.Unix(unix, 0).UTC()
	}
	return Stats{
		ScansTotal:    metricScans.Value(),
		LastScanAt:    last,
		LastScanMs:    metricScanMillis.Value(),
		StaleUsers:    metricStaleUsers.Value(),
		OfflineSent:   metricOfflineSent.Value(),
		OfflineErrors: metricOfflineErrors.Value(),
		SleepSent:     metricSleepSent.Value(),
		ScanEvery:     w.cfg.ScanEvery,
	}
}

// Verdict -- что сказать оператору одной строкой.
//
// Отставание считается в шагах обхода, а не в секундах: обход раз в тридцать
// секунд и обход раз в пять минут -- разные нормы, и одна константа соврала бы
// про обе.
type Verdict struct {
	Alive  bool
	Reason string
}

const (
	// Один пропущенный шаг -- это ещё не смерть: обход мог затянуться на
	// медленной базе. Три пропущенных подряд объяснить уже нечем.
	staleScanFactor = 3
)

func Judge(s Stats, now time.Time) Verdict {
	if s.ScansTotal == 0 || s.LastScanAt.IsZero() {
		return Verdict{Alive: false, Reason: "сторож ещё ни разу не обошёл парк"}
	}
	every := s.ScanEvery
	if every <= 0 {
		every = defaultScanEvery
	}
	age := now.Sub(s.LastScanAt)
	if age > time.Duration(staleScanFactor)*every {
		return Verdict{Alive: false, Reason: "сторож не обходил парк " + age.Round(time.Second).String()}
	}
	return Verdict{Alive: true, Reason: "обход " + age.Round(time.Second).String() + " назад"}
}
