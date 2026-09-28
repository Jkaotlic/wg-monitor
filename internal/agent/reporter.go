package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const perCheckTimeout = 10 * time.Second

// ResumedThreshold: a gap longer than this between successful reports flags
// the next report as Resumed=true. Mobile routers (in cars) can miss a few
// regular 60s ticks while moving between cells; treat only a sleep-scale gap
// as "back online".
const ResumedThreshold = 30 * time.Minute

// reporterStatePersistEvery -- как часто рутинный успешный отчёт переписывает
// файл состояния (AGENT-10: раньше -- каждый отчёт, 1440 записей во флеш в
// сутки). Сохранённое время отстаёт не больше чем на этот срок -- на фоне
// ResumedThreshold (30 мин) это допуск, а при штатной остановке Run пишет
// точное время. Значимые перемены (отказ авторизации, выздоровление)
// пишутся сразу.
const reporterStatePersistEvery = 5 * time.Minute

type Sender interface {
	// SendReport posts a heartbeat report and returns the backend's canonical URL
	// (empty string if the server did not advertise one).
	SendReport(ctx context.Context, r wire.Report) (string, error)
}

// FactsProvider -- блок фактов отчёта (v0.47, internal/agent/facts). Collect
// зовётся на каждый отчёт; Committed -- только после ответа 2xx.
type FactsProvider interface {
	Collect(ctx context.Context) *wire.ReportFacts
	Committed(sent *wire.ReportFacts)
}

// factsCollectTimeout -- факты читают awg-manager (журнал пингчека, WAN);
// зависший ответ не должен держать отчёт.
const factsCollectTimeout = 8 * time.Second

// reporterMigrateURL is a var so tests can replace it without touching the
// filesystem or spawning processes.
var reporterMigrateURL = func(ctx context.Context, newURL, configPath string) (string, error) {
	return actions.UpdateBackendURL(ctx, newURL, configPath)
}

type Reporter struct {
	sender       Sender
	version      string
	interval     time.Duration
	checks       []checks.Check
	multiChecks  []checks.MultiCheck
	deps         checks.Deps
	awgClient    *awgmgr.Client // optional, used to force fresh pingcheck on resume
	statePath    string         // persists last_report_at across agent restarts
	configPath   string         // path to config.yaml; enables auto URL migration
	backendURL   string         // current configured backend URL for migration comparison
	mu           sync.Mutex
	sendOnceMu   sync.Mutex // serialises sendOnce; see sendOnce comment
	lastReportAt time.Time
	forceResumed bool // set by ForceResumed; consumed by next sendOnce

	trigger string        // wire.TriggerHook для внеочередного отчёта; съедается sendOnce
	facts   FactsProvider // nil -- отчёт без фактов
	wake    chan struct{}

	lastAuthErrorAt        time.Time
	consecutiveAuthRejects int

	reportOKPath    string // AGENT-11: метка «новый бинарь отчитался» для скрипта замены
	reportOKWritten bool
	// reportRejectedPath -- метка «бэкенд явно отверг отчёт» (4xx).
	reportRejectedPath    string
	reportRejectedWritten bool

	lastPersistAt time.Time // AGENT-10: когда файл состояния писался последний раз
	stateWrites   int       // сколько раз писался (для тестов)
}

type ReporterConfig struct {
	Sender      Sender
	Version     string
	Interval    time.Duration
	Checks      []checks.Check
	MultiChecks []checks.MultiCheck
	Deps        checks.Deps
	AwgClient   *awgmgr.Client
	StatePath   string // e.g. /opt/var/wg-monitor/reporter-state.json; "" disables persistence
	ConfigPath  string // e.g. /opt/etc/wg-monitor/config.yaml; enables auto URL migration
	BackendURL  string // current backend URL from config; compared against canonical_url
	// ReportOKPath -- куда положить метку после первого успешного отчёта
	// процесса (actions.SelfUpdateReportOKPath). Пусто -- не писать.
	ReportOKPath string
	// ReportRejectedPath -- куда положить метку, когда бэкенд явно отверг
	// отчёт (ErrReportRejected: JSON-ответ 4xx, включая 401/403). Сеть и 5xx её не ставят:
	// авария бэкенда -- не повод откатывать обновление. Пусто -- не писать.
	ReportRejectedPath string
	Facts              FactsProvider
}

func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	r := &Reporter{
		sender:      cfg.Sender,
		version:     cfg.Version,
		interval:    cfg.Interval,
		checks:      cfg.Checks,
		multiChecks: cfg.MultiChecks,
		deps:        cfg.Deps,
		awgClient:   cfg.AwgClient,
		statePath:   cfg.StatePath,
		configPath:  cfg.ConfigPath,
		backendURL:  cfg.BackendURL,

		reportOKPath:       cfg.ReportOKPath,
		reportRejectedPath: cfg.ReportRejectedPath,
		facts:              cfg.Facts,
		wake:               make(chan struct{}, 1),
	}
	r.loadState()
	return r
}

func (r *Reporter) Run(ctx context.Context) {
	r.sendOnce(ctx)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.persistOnExit()
			return
		case <-t.C:
			r.sendOnce(ctx)
		case <-r.wake:
			if r.wakeReport(ctx) {
				t.Reset(r.interval)
			}
		}
	}
}

// ForceResumed triggers an immediate report cycle with Resumed=true regardless
// of last-report timing. Wired into the cmdloop force_recheck action so an
// admin can poke a mobile router into a fresh report from TG.
func (r *Reporter) ForceResumed(ctx context.Context) {
	r.mu.Lock()
	r.forceResumed = true
	r.mu.Unlock()
	r.sendOnce(ctx)
}

// RequestWake -- хук KeenOS увидел смену интерфейса (v0.47). Не блокирует:
// второй запрос, пока первый не взят, ничего не добавляет.
func (r *Reporter) RequestWake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// wakeReport шлёт внеочередной отчёт с trigger=hook, только если бэкенд
// объявил, что пропустит его мимо автомата тревог.
func (r *Reporter) wakeReport(ctx context.Context) bool {
	hs, ok := r.sender.(interface{ HookReportsAllowed() bool })
	if !ok || !hs.HookReportsAllowed() {
		return false
	}
	r.mu.Lock()
	r.trigger = wire.TriggerHook
	r.mu.Unlock()
	r.sendOnce(ctx)
	return true
}

// sendMu serialises sendOnce — both the scheduled tick and ForceResumed call
// sendOnce. Without serialisation, concurrent invocations issue duplicate
// /v1/report POSTs and pollute events with same-timestamp duplicates.
func (r *Reporter) sendOnce(ctx context.Context) {
	r.sendOnceMu.Lock()
	defer r.sendOnceMu.Unlock()
	r.sendOnceLocked(ctx)
}

func (r *Reporter) sendOnceLocked(ctx context.Context) {
	start := time.Now()

	r.mu.Lock()
	prev := r.lastReportAt
	forced := r.forceResumed
	r.forceResumed = false
	trigger := r.trigger
	r.trigger = ""
	r.mu.Unlock()
	resumed := forced || (!prev.IsZero() && time.Since(prev) > ResumedThreshold)

	if resumed && r.awgClient != nil {
		// Kick awg-manager into a fresh ping cycle so our /pingcheck/status
		// fetch a moment later returns up-to-date failCount/restartCount.
		// Best-effort: ignore errors and continue with whatever we have.
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if err := r.awgClient.PingCheckNow(cctx); err != nil {
			slog.Debug("force pingcheck on resume failed", "err", err)
		}
		cancel()
		// Brief settle window so awg-manager's internal state reflects the kick.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}

	results := r.runAll(ctx)
	results = append(results, wire.Check{
		Name:       "agent_heartbeat",
		Status:     "ok",
		DurationMs: time.Since(start).Milliseconds(),
	})

	var facts *wire.ReportFacts
	if r.facts != nil {
		fctx, cancel := context.WithTimeout(ctx, factsCollectTimeout)
		facts = r.facts.Collect(fctx)
		cancel()
	}

	report := wire.Report{
		Timestamp:    start.UTC(),
		AgentVersion: r.version,
		Checks:       results,
		Resumed:      resumed,
		Facts:        facts,
		Trigger:      trigger,
	}
	canonicalURL, err := r.sender.SendReport(ctx, report)
	if err != nil {
		slog.Warn("send report failed", "err", err)
		if errors.Is(err, ErrReportRejected) {
			r.markReportRejected()
		}
		if errors.Is(err, ErrUnauthorized) {
			now := time.Now()
			r.mu.Lock()
			r.consecutiveAuthRejects++
			r.lastAuthErrorAt = now
			snap := reporterState{
				LastReportAt:           r.lastReportAt,
				LastAuthErrorAt:        r.lastAuthErrorAt,
				ConsecutiveAuthRejects: r.consecutiveAuthRejects,
			}
			firstReject := r.consecutiveAuthRejects == 1
			r.mu.Unlock()
			r.persistStateThrottled(snap, firstReject)
		}
		return
	}
	if canonicalURL != "" && canonicalURL != r.backendURL && r.configPath != "" {
		slog.Info("backend advertised new canonical URL; migrating config",
			"old_url", r.backendURL, "new_url", canonicalURL)
		if _, merr := reporterMigrateURL(ctx, canonicalURL, r.configPath); merr != nil {
			slog.Warn("auto URL migration failed", "err", merr)
		} else {
			r.backendURL = canonicalURL
		}
	}
	now := time.Now()
	r.mu.Lock()
	recovered := r.consecutiveAuthRejects > 0 || !r.lastAuthErrorAt.IsZero()
	r.lastReportAt = now
	r.consecutiveAuthRejects = 0
	r.lastAuthErrorAt = time.Time{}
	snap := reporterState{LastReportAt: now}
	r.mu.Unlock()
	r.persistStateThrottled(snap, recovered)
	r.markReportOK()
	if r.facts != nil && facts != nil {
		r.facts.Committed(facts)
	}
}

// markReportRejected -- AGENT-11: бэкенд явно отверг отчёт. Скрипт замены
// откатывает бинарь, если за 5 минут так и не было успешного отчёта.
func (r *Reporter) markReportRejected() {
	if r.reportRejectedPath == "" || r.reportRejectedWritten {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.reportRejectedPath), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(r.reportRejectedPath, []byte(r.version+"\n"), 0o644); err != nil {
		slog.Debug("report-rejected marker write", "err", err)
		return
	}
	r.reportRejectedWritten = true
}

// markReportOK -- AGENT-11: после первого успешного отчёта процесса кладёт
// метку, по которой скрипт замены бинаря решает «обновление живо» (иначе --
// откат). Один раз за процесс: скрипт стирает метку перед запуском нового
// бинаря, а лишние записи во флеш не нужны.
func (r *Reporter) markReportOK() {
	if r.reportOKPath == "" || r.reportOKWritten {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.reportOKPath), 0o755); err != nil {
		slog.Debug("report-ok marker mkdir", "err", err)
		return
	}
	if err := os.WriteFile(r.reportOKPath, []byte(r.version+"\n"), 0o644); err != nil {
		slog.Debug("report-ok marker write", "err", err)
		return
	}
	r.reportOKWritten = true
}

func (r *Reporter) runAll(parent context.Context) []wire.Check {
	var (
		mu  sync.Mutex
		out []wire.Check
		wg  sync.WaitGroup
	)
	for _, c := range r.checks {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			res := c.Run(ctx, r.deps)
			mu.Lock()
			out = append(out, res)
			mu.Unlock()
		}()
	}
	for _, mc := range r.multiChecks {
		mc := mc
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, perCheckTimeout)
			defer cancel()
			res := mc.Run(ctx, r.deps)
			mu.Lock()
			out = append(out, res...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

type reporterState struct {
	LastReportAt           time.Time `json:"last_report_at"`
	LastAuthErrorAt        time.Time `json:"last_auth_error_at,omitzero"`
	ConsecutiveAuthRejects int       `json:"consecutive_auth_rejects,omitzero"`
}

func (r *Reporter) loadState() {
	if r.statePath == "" {
		return
	}
	body, err := os.ReadFile(r.statePath)
	if err != nil {
		return
	}
	var s reporterState
	if err := json.Unmarshal(body, &s); err != nil {
		// State file existed but was corrupt: surface so accumulated stale
		// `.tmp` artefacts or aborted writes are diagnosable.
		slog.Warn("reporter state corrupt; treating agent as freshly-started", "path", r.statePath, "err", err)
		return
	}
	r.lastReportAt = s.LastReportAt
	r.lastAuthErrorAt = s.LastAuthErrorAt
	r.consecutiveAuthRejects = s.ConsecutiveAuthRejects
}

// persistStateThrottled пишет состояние, если перемена значимая (force), это
// первая запись процесса или с прошлой прошло reporterStatePersistEvery.
func (r *Reporter) persistStateThrottled(s reporterState, force bool) {
	if !force && !r.lastPersistAt.IsZero() && time.Since(r.lastPersistAt) < reporterStatePersistEvery {
		return
	}
	r.persistState(s)
}

// persistOnExit -- при штатной остановке файл получает точное время
// последнего отчёта: троттлинг не должен сдвигать порог «вернулся после сна».
func (r *Reporter) persistOnExit() {
	r.sendOnceMu.Lock()
	defer r.sendOnceMu.Unlock()
	r.mu.Lock()
	snap := reporterState{
		LastReportAt:           r.lastReportAt,
		LastAuthErrorAt:        r.lastAuthErrorAt,
		ConsecutiveAuthRejects: r.consecutiveAuthRejects,
	}
	r.mu.Unlock()
	if snap.LastReportAt.IsZero() && snap.ConsecutiveAuthRejects == 0 {
		return
	}
	r.persistState(snap)
}

func (r *Reporter) persistState(s reporterState) {
	if r.statePath == "" {
		return
	}
	r.lastPersistAt = time.Now()
	r.stateWrites++
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o755); err != nil {
		slog.Debug("reporter state mkdir", "err", err)
		return
	}
	body, _ := json.Marshal(s)
	tmp := r.statePath + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		slog.Debug("reporter state write", "err", err)
		return
	}
	if err := os.Rename(tmp, r.statePath); err != nil {
		slog.Debug("reporter state rename", "err", err)
	}
}
