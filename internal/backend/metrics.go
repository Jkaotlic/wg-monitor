package backend

import (
	"expvar"
	"runtime"
	"sync/atomic"
)

// Lightweight metrics surfaced via /debug/vars (expvar — zero deps). Real
// Prometheus scraping is deferred until fleet >100; for the current ~10
// agents these counters answer the questions we actually ask: "how many
// reports last hour", "any 401 spike", "TG send failure rate", etc.
// (OBS-13).
//
// All atomic for concurrent ingestion. Callers .Add(1) on the appropriate
// counter; readers go through expvar's HTTP handler.

var (
	metricReportsTotal       = expvar.NewInt("wgm_reports_total")
	metricReportsRateLimited = expvar.NewInt("wgm_reports_rate_limited_total")
	metricReportsDuplicate   = expvar.NewInt("wgm_report_duplicate_events_total")
	metricAuthRejected       = expvar.NewInt("wgm_auth_rejected_total")
	metricCmdEnqueued        = expvar.NewInt("wgm_cmd_enqueued_total")
	metricCmdResultRecorded  = expvar.NewInt("wgm_cmd_result_recorded_total")
	metricCmdResultRelayed   = expvar.NewInt("wgm_cmd_result_relayed_total")
	metricTGSendErrors       = expvar.NewInt("wgm_tg_send_errors_total")
)

// Число горутин -- дешёвый детектор «фоновый цикл умер или залип». Утечка
// растёт, смерть цикла видна как проседание; без этого единственным способом
// узнать, жив ли сторож, был неприехавший алерт.
func init() {
	expvar.Publish("wgm_goroutines", expvar.Func(func() any { return runtime.NumGoroutine() }))
}

// Per-package callers go through these typed wrappers so renaming a metric
// doesn't ripple. The closure form also lets us swap to atomic-int64 later
// without touching call sites.

func incReports()         { metricReportsTotal.Add(1) }
func incReportRateLimit() { metricReportsRateLimited.Add(1) }
func addReportDup(n int)  { metricReportsDuplicate.Add(int64(n)) }
func incAuthReject()      { metricAuthRejected.Add(1) }
func incCmdEnqueued()     { metricCmdEnqueued.Add(1) }
func incCmdResult()       { metricCmdResultRecorded.Add(1) }
func incCmdResultRelay()  { metricCmdResultRelayed.Add(1) }
func incTGError()         { metricTGSendErrors.Add(1) }

// reportSeenTracker remembers the last "info-logged" timestamp per nickname
// so OBS-14 sampling doesn't spam INFO every 60s for every agent. Status
// changes always log; otherwise we log every Nth report. Tracks via atomic
// counters keyed by nickname (small fleet, bounded N).
type reportSeenTracker struct{ counter atomic.Int64 }

// shouldLogReport returns true on the 1st report and every 10th thereafter.
// Status-change detection lives in the dispatcher's FSM transition log
// (OBS-09) so this sampler is purely about heartbeat-traffic noise.
func (t *reportSeenTracker) shouldLogReport() bool {
	n := t.counter.Add(1)
	return n == 1 || n%10 == 0
}

var globalReportTracker = &reportSeenTracker{}
