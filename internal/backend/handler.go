package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/pkg/wire"
)

const (
	maxReportBytes  = 64 * 1024
	maxResultBytes  = 16 * 1024
	defaultCmdWait  = 30 * time.Second
	maxCmdWait      = 60 * time.Second
)

type Dispatcher interface {
	Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, check wire.Check) error
}

// Resumer is a back-edge from /v1/report into the heartbeat watcher.
// When an agent reports Resumed=true (just rejoined after a gap), the
// watcher needs to know so it can suppress a spurious OFFLINE alert
// during the resume-grace window. Implemented by *heartbeat.Watcher;
// tests can pass nil.
type Resumer interface {
	MarkResumed(userID int64)
}

// CommandSink is the subset of cmd.Queue used by the HTTP handlers.
// Decoupled so tests can swap in a fake.
type CommandSink interface {
	Dequeue(ctx context.Context, userID int64, holdTimeout time.Duration) (*wire.Command, bool)
	RecordResult(userID int64, result wire.CommandResult) error
	ConsumeOriginRef(userID int64, cmdID string) (cmdpkg.MessageRef, bool)
}

// TGNotifier posts command-result text back to the originating TG message.
// Implemented by callbacks.Notifier; nil-safe (handler skips relay if absent).
type TGNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, maxChars int) error
}

// RoutesNotifier is the subset used by cmdResultHandler when ref.Action is
// route_status or route_rebind. Implemented by callbacks.RoutesPanelNotifier.
type RoutesNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error
}

// MaintNotifier is the subset used by cmdResultHandler when ref.Action is
// version_audit / firmware_status / service_restart / firmware_install.
// Implemented by callbacks.MaintPanelNotifier.
type MaintNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error
}

type Deps struct {
	Logger         *slog.Logger
	DB             *db.DB
	Dispatcher     Dispatcher
	Resumer        Resumer
	CommandSink    CommandSink
	TGNotifier     TGNotifier
	RoutesNotifier RoutesNotifier // nil-safe (handler skips if nil)
	MaintNotifier  MaintNotifier  // nil-safe (handler skips if nil)
	UI             UIConfig
	Thresholds     state.Thresholds
}

func NewMux(d Deps) http.Handler {
	mux := http.NewServeMux()
	// /healthz: liveness only — process is up & accepting HTTP. Caddy uses
	// it for upstream health, no DB checks.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	// /readyz: deep health — DB ping + sanity checks. External monitoring
	// (smoke/doctor/cron) should use this. 503 means "process is up but
	// can't service requests" — restart, page operator, etc.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db not configured", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.SQL().PingContext(ctx); err != nil {
			d.Logger.Warn("readyz db ping failed", "err", err)
			http.Error(w, "db ping: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ready\n")
	})
	auth := AuthMiddleware(d.DB.Users(), d.Logger)
	mux.Handle("/v1/report", auth(http.HandlerFunc(reportHandler(d))))
	if d.CommandSink != nil {
		mux.Handle("/v1/cmd", auth(http.HandlerFunc(cmdGetHandler(d))))
		mux.Handle("/v1/cmd/result", auth(http.HandlerFunc(cmdResultHandler(d))))
	}
	return mux
}

func reportHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes+1))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(body) > maxReportBytes {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var rep wire.Report
		if err := json.Unmarshal(body, &rep); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())

		// Resumed=true means the agent self-detected a gap (mobile rejoin).
		// Tell the watcher BEFORE running the FSM so a near-simultaneous
		// scan-tick won't fire a spurious OFFLINE while we ingest the
		// freshly-collected checks.
		if rep.Resumed && d.Resumer != nil {
			d.Resumer.MarkResumed(uid)
		}
		ts := rep.Timestamp
		if ts.IsZero() {
			ts = time.Now().UTC()
		}

		// Ingest всех событий + UpdateLastSeen — одной транзакцией. Раньше
		// был N+1 без atomicity: краш или ctx.Cancel в середине цикла
		// оставлял часть events записанными без advance LastSeen, или
		// наоборот; FSM этих агентов оставался "глух" до следующего отчёта.
		// Транзакция гарантирует "всё или ничего" по части ingest-а; FSM-
		// dispatch делается ОТДЕЛЬНО после commit'а — он имеет TG side
		// effects, обернуть в DB-tx нельзя.
		tx, err := d.DB.SQL().BeginTx(r.Context(), nil)
		if err != nil {
			d.Logger.Warn("report tx begin", "nickname", nick, "err", err)
			http.Error(w, "tx begin", http.StatusInternalServerError)
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			"UPDATE users SET last_seen_at = ? WHERE id = ?", ts, uid); err != nil {
			tx.Rollback()
			d.Logger.Warn("update last_seen", "nickname", nick, "err", err)
			http.Error(w, "update last_seen", http.StatusInternalServerError)
			return
		}
		insertStmt, err := tx.PrepareContext(r.Context(),
			`INSERT INTO events(user_id, check_name, status, details_json, ts) VALUES(?,?,?,?,?)`)
		if err != nil {
			tx.Rollback()
			d.Logger.Warn("report tx prepare", "nickname", nick, "err", err)
			http.Error(w, "prepare", http.StatusInternalServerError)
			return
		}
		for _, c := range rep.Checks {
			detailsJSON, _ := json.Marshal(c.Details)
			if _, err := insertStmt.ExecContext(r.Context(), uid, c.Name, c.Status, string(detailsJSON), ts); err != nil {
				insertStmt.Close()
				tx.Rollback()
				d.Logger.Warn("event insert (tx rollback)", "nickname", nick, "check", c.Name, "err", err)
				http.Error(w, "event insert", http.StatusInternalServerError)
				return
			}
		}
		insertStmt.Close()
		if err := tx.Commit(); err != nil {
			d.Logger.Warn("report tx commit", "nickname", nick, "err", err)
			http.Error(w, "commit", http.StatusInternalServerError)
			return
		}

		// Post-commit: FSM dispatch. Каждая итерация — отдельный State.Save
		// внутри Dispatcher.Handle (single-statement, атомарная per check).
		// Если dispatch падает (TG 5xx), state уже сохранён до TG-send
		// после rc19 fix BUG-02 — следующий report не дублирует alert.
		for _, c := range rep.Checks {
			if c.Name == "agent_heartbeat" {
				continue
			}
			prev, err := d.DB.State().Get(uid, c.Name)
			if err != nil {
				d.Logger.Warn("state.Get", "err", err)
				continue
			}
			tr := state.Apply(prev, c.Status, time.Now(), d.Thresholds)
			// FSM transition timeline for post-mortem (OBS-09). Hard/Recovery
			// stay at Info; SoftFlap is Debug to avoid noise on transient flaps.
			switch tr.Kind {
			case state.Hard, state.Recovery:
				d.Logger.Info("fsm transition",
					"nickname", nick, "check", c.Name,
					"kind", tr.Kind.String(),
					"prev_status", prev.CurrentStatus, "next_status", tr.Next.CurrentStatus,
					"consecutive_fails", tr.Next.ConsecutiveFails,
				)
			case state.SoftFlap:
				d.Logger.Debug("fsm transition",
					"nickname", nick, "check", c.Name, "kind", tr.Kind.String(),
					"consecutive_fails", tr.Next.ConsecutiveFails,
				)
			}
			if err := d.Dispatcher.Handle(r.Context(), uid, nick, c.Name, tr, c); err != nil {
				d.Logger.Warn("dispatch", "check", c.Name, "kind", tr.Kind, "err", err)
			}
		}
		d.Logger.Info("report",
			"nickname", nick, "agent_version", rep.AgentVersion,
			"resumed", rep.Resumed,
			"check_count", len(rep.Checks), "checks", checkSummary(rep.Checks),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func checkSummary(checks []wire.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name + "=" + c.Status
	}
	return out
}

// cmdGetHandler implements long-poll dequeue. ?wait=N caps the hold window
// (seconds, default 30, max 60). 200+JSON when a command is ready, 204 on
// timeout. Auth context provides the userID — agents only see their own queue.
func cmdGetHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		wait := defaultCmdWait
		if v := r.URL.Query().Get("wait"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				http.Error(w, "bad wait", http.StatusBadRequest)
				return
			}
			wait = time.Duration(n) * time.Second
			if wait > maxCmdWait {
				wait = maxCmdWait
			}
		}
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())
		c, ok := d.CommandSink.Dequeue(r.Context(), uid, wait)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, err := json.Marshal(c)
		if err != nil {
			d.Logger.Error("cmd marshal", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		d.Logger.Info("cmd dispatched", "nickname", nick, "cmd_id", c.ID, "action", c.Action)
	}
}

func cmdResultHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxResultBytes+1))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if len(body) > maxResultBytes {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		var res wire.CommandResult
		if err := json.Unmarshal(body, &res); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if res.ID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		// Status: log-and-accept для unknown значений. Раньше backend
		// возвращал 400 на любой статус не из whitelist'а — это ломало
		// schema evolution: новый агент с дополнительным статусом ("partial",
		// "rate_limited", и т.п.) терял результат, пока backend не обновлён.
		// Postel's law для внутреннего API: liberal in what we accept.
		if res.Status == "" {
			http.Error(w, "status required", http.StatusBadRequest)
			return
		}
		if !wire.IsValidCommandResultStatus(res.Status) {
			d.Logger.Warn("cmd result with unknown status — accepting forward-compat",
				"status", res.Status, "cmd_id", res.ID,
				"nickname", NicknameFromContext(r.Context()))
		}
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())
		if err := d.CommandSink.RecordResult(uid, res); err != nil {
			d.Logger.Warn("cmd result record", "nickname", nick, "err", err)
			http.Error(w, "record failed", http.StatusInternalServerError)
			return
		}
		// Relay result back to TG (or routes notifier) if a notifier is configured
		// and we recorded the originating message. Async — must not stall the
		// agent's POST on TG network latency.
		if ref, ok := d.CommandSink.ConsumeOriginRef(uid, res.ID); ok {
			switch ref.Action {
			case "route_status", "route_rebind":
				if d.RoutesNotifier != nil {
					go func(ref cmdpkg.MessageRef, res wire.CommandResult) {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := d.RoutesNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
							d.Logger.Warn("routes notifier failed", "cmd_id", res.ID, "action", ref.Action, "err", err)
						}
					}(ref, res)
				} else {
					d.Logger.Warn("routes notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			case "version_audit", "firmware_status", "service_restart", "firmware_install":
				if d.MaintNotifier != nil {
					go func(ref cmdpkg.MessageRef, res wire.CommandResult) {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := d.MaintNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
							d.Logger.Warn("maint notifier failed", "cmd_id", res.ID, "action", ref.Action, "err", err)
						}
					}(ref, res)
				} else {
					d.Logger.Warn("maint notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			default:
				if d.TGNotifier != nil {
					maxChars := d.UI.DiagMaxChars
					if maxChars == 0 {
						maxChars = 3500
					}
					go func(ref cmdpkg.MessageRef, res wire.CommandResult, maxChars int) {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, maxChars); err != nil {
							d.Logger.Warn("tg notify failed", "cmd_id", res.ID, "err", err)
						}
					}(ref, res, maxChars)
				} else {
					d.Logger.Warn("tg notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			}
		}
		d.Logger.Info("cmd result", "nickname", nick, "cmd_id", res.ID, "status", res.Status, "duration_ms", res.DurationMs)
		w.WriteHeader(http.StatusOK)
	}
}
