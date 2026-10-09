package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// upsertReportPingRuns пишет серии пингчека в транзакции приёма отчёта: если
// отчёт не лёг, не легли и серии, и агент пришлёт их снова (курсор у него
// двигается только после 200).
func upsertReportPingRuns(ctx context.Context, tx *sql.Tx, uid int64, f *wire.ReportFacts) (int, error) {
	if f == nil || len(f.PingRuns) == 0 {
		return 0, nil
	}
	runs := f.PingRuns
	if len(runs) > wire.MaxPingRuns {
		runs = runs[:wire.MaxPingRuns]
	}
	n := 0
	for _, run := range runs {
		id := strings.TrimSpace(run.TunnelID)
		if id == "" || run.From.IsZero() {
			continue
		}
		to := run.To
		if to.Before(run.From) {
			to = run.From
		}
		row := db.PingRunRow{
			TunnelID: id, TunnelName: wire.ClipText(run.TunnelName),
			From: run.From, To: to, Fails: run.Fails,
			WentDown: run.WentDown, Recovered: run.Recovered, Error: wire.ClipText(run.Error),
		}
		if _, err := tx.ExecContext(ctx, db.UpsertPingRunSQL, db.PingRunArgs(uid, row)...); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// saveReportFacts кладёт блоки фактов после commit, как снимок версий: факт --
// удобство экрана, а не причина отвергнуть отчёт, поэтому ошибка только в журнал.
func saveReportFacts(d Deps, uid int64, nick string, f *wire.ReportFacts, now time.Time) {
	put := func(kind string, v any, at time.Time) {
		if at.IsZero() {
			at = now
		}
		body, err := json.Marshal(v)
		if err != nil {
			d.Logger.Warn("report facts encode", "nickname", nick, "kind", kind, "err", err)
			return
		}
		if err := d.DB.RouterFacts().Upsert(uid, kind, body, at, now); err != nil {
			d.Logger.Warn("report facts upsert", "nickname", nick, "kind", kind, "err", err)
		}
	}
	if f.Exit != nil {
		put(db.FactExit, f.Exit, f.Exit.At)
	}
	if f.WAN != nil {
		put(db.FactWAN, f.WAN, f.WAN.At)
	}
	if f.NativeDNS != nil {
		put(db.FactNativeDNS, f.NativeDNS, f.NativeDNS.At)
	}
	if f.Hooks != nil {
		put(db.FactHooks, f.Hooks, f.Hooks.At)
	}
	if f.PingLog != nil {
		put(db.FactPingLog, f.PingLog, f.PingLog.At)
	}
}

// unstickNotifyMaxAge -- «починил» старше часа владельцу уже не новость
// (агент долго не мог отчитаться); событие всё равно записывается.
const unstickNotifyMaxAge = time.Hour

// insertReportUnstickEvents пишет события сторожа зависаний в транзакции
// приёма отчёта и возвращает те, о которых надо сообщить владельцу: новые
// (вставка легла), fixed, не старше часа.
func insertReportUnstickEvents(ctx context.Context, tx *sql.Tx, uid int64, f *wire.ReportFacts, now time.Time) ([]wire.UnstickEvent, error) {
	if f == nil || f.Unstick == nil {
		return nil, nil
	}
	evs := f.Unstick.Events
	if len(evs) > wire.MaxUnstickEvents {
		evs = evs[len(evs)-wire.MaxUnstickEvents:]
	}
	var notify []wire.UnstickEvent
	for _, e := range evs {
		if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.TunnelID) == "" || e.At.IsZero() {
			continue
		}
		res, err := tx.ExecContext(ctx, db.InsertUnstickEventSQL, db.UnstickEventArgs(uid, e)...)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 && e.Result == wire.UnstickFixed && now.Sub(e.At) <= unstickNotifyMaxAge {
			notify = append(notify, e)
		}
	}
	return notify, nil
}
