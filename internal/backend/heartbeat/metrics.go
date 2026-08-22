// internal/backend/heartbeat/metrics.go
package heartbeat

import "expvar"

// Сторож молчит в двух случаях: когда в парке всё хорошо и когда он сам мёртв.
// Снаружи -- в логах, в БД, в Telegram -- эти два случая до сих пор выглядели
// одинаково, и отличить их можно было только задним числом, по неприехавшему
// алерту. Счётчики ниже делают разницу видимой: обход парка отмечается всегда,
// даже когда докладывать не о чем.
//
// Читаются через /debug/vars (loopback), стоят один Add на обход.
var (
	metricScans         = expvar.NewInt("wgm_heartbeat_scans_total")
	metricLastScanUnix  = expvar.NewInt("wgm_heartbeat_last_scan_unix")
	metricScanMillis    = expvar.NewInt("wgm_heartbeat_last_scan_ms")
	metricStaleUsers    = expvar.NewInt("wgm_heartbeat_stale_users")
	metricOfflineSent   = expvar.NewInt("wgm_heartbeat_offline_sent_total")
	metricOfflineErrors = expvar.NewInt("wgm_heartbeat_offline_errors_total")
	metricSleepSent     = expvar.NewInt("wgm_heartbeat_sleep_sent_total")
	// Сколько просроченных роутеров обход пропустил намеренно -- из-за ack
	// или тишины, выставленной оператором. Без этого числа «увидел четверых,
	// не отправил ни одной тревоги» выглядит поломкой, хотя может быть
	// послушанием.
	metricSuppressed = expvar.NewInt("wgm_heartbeat_suppressed_users")
)
