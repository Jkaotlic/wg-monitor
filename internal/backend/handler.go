package backend

import (
	"context"
	"encoding/json"
	"expvar"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	maxReportBytes = 64 * 1024
	maxResultBytes = 16 * 1024
	defaultCmdWait = 30 * time.Second
	maxCmdWait     = 60 * time.Second
)

// Version is set by main.Version via SetVersion at startup so /healthz can
// surface it without circular import. Defaults to "unknown" if unset.
var serverVersion = "unknown"

// SetVersion is called from main with the build version string.
func SetVersion(v string) { serverVersion = v }

// errCode → HTTP status mapping for writeJSONError. Codes are wire-stable
// (see pkg/wire/errors.md once we ship one); HTTP status follows API-03.
const (
	errCodeBadJSON       = "bad_json"
	errCodeIDRequired    = "id_required"
	errCodeStatusReq     = "status_required"
	errCodeBodyTooLarge  = "body_too_large"
	errCodeMethodNotAll  = "method_not_allowed"
	errCodeInvalidWait   = "invalid_wait"
	errCodeUnsupportedCT = "unsupported_content_type"
	errCodeInternal      = "internal"
)

// writeJSONError emits {code,message} JSON with the given HTTP status. Used by
// /v1/* endpoints (API-02). /healthz keeps text/plain for k8s-style probes.
func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

// requireJSONContentType enforces application/json (or empty) on POST bodies
// (API-08). Empty Content-Type stays accepted for backward compat with old
// agents that didn't set the header. Returns true on success.
func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return true
	}
	// Strip parameters (`application/json; charset=utf-8`).
	if i := indexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = trimSpace(ct)
	if ct != "application/json" {
		writeJSONError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedCT,
			"expected application/json")
		return false
	}
	return true
}

// normaliseDetailsJSON canonicalises a check's details map for storage. nil
// or empty map → empty string (NULL-equivalent semantically; cheaper to scan
// than `"null"` literal). DB-13: previously `json.Marshal(nil)` produced the
// literal string `"null"` which downstream readers had to special-case.
func normaliseDetailsJSON(details map[string]any) string {
	if len(details) == 0 {
		return ""
	}
	b, err := json.Marshal(details)
	if err != nil || string(b) == "null" {
		return ""
	}
	return string(b)
}

// relayParent returns the parent context for cmdResultHandler relay goroutines:
// d.ShutdownCtx if wired, else context.Background. Wired path lets srv.Shutdown
// signal in-flight TG sends instead of letting them outlive the server.
func relayParent(d Deps) context.Context {
	if d.ShutdownCtx != nil {
		return d.ShutdownCtx
	}
	return context.Background()
}

// trimSpace + indexByte are tiny one-line helpers to avoid importing strings
// only for two calls — handler.go already pulls many deps. Using stdlib here
// would also be fine.
func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

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

// WakeNotifier is fired from /v1/report when an agent's Report carries
// Resumed=true AND the user is kind=mobile. It renders the adaptive 🚗
// card and posts to the router's TG-topic. nil-safe (handler skips).
type WakeNotifier interface {
	SendWake(ctx context.Context, userID int64, nickname string, checks []wire.Check) error
}

// CommandSink is the subset of cmd.Queue used by the HTTP handlers.
// Decoupled so tests can swap in a fake.
type CommandSink interface {
	Dequeue(ctx context.Context, userID int64, holdTimeout time.Duration) (*wire.Command, bool)
	RecordResult(userID int64, result wire.CommandResult) error
	ConsumeOriginRef(userID int64, cmdID string) (cmdpkg.MessageRef, bool)
	Enqueue(userID int64, cmd wire.Command) error
	AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool)
}

// TGNotifier posts command-result text back to the originating TG message.
// Implemented by callbacks.Notifier; nil-safe (handler skips relay if absent).
type TGNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, userID int64, maxChars int) error
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

// OpkgNotifier is the subset used by cmdResultHandler when ref.Action is
// opkg_upgrade or opkg_feed_disable. Implemented by callbacks.Notifier via
// the NotifyOpkgResult method. Receives userID so it can register pending
// repair entries per FailedFeed URL before sending the TG message with
// the inline 🔧 buttons.
type OpkgNotifier interface {
	NotifyOpkgResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64, maxChars int) error
}

// PingCheckNotifier is the subset used by cmdResultHandler when ref.Action is
// pingcheck_status or pingcheck_toggle. Implemented by callbacks.PingCheckPanelNotifier.
type PingCheckNotifier interface {
	NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error
}

type Deps struct {
	Logger            *slog.Logger
	DB                *db.DB
	Dispatcher        Dispatcher
	Resumer           Resumer
	CommandSink       CommandSink
	TGNotifier        TGNotifier
	RoutesNotifier    RoutesNotifier    // nil-safe (handler skips if nil)
	MaintNotifier     MaintNotifier     // nil-safe (handler skips if nil)
	OpkgNotifier      OpkgNotifier      // nil-safe (handler falls back to TGNotifier if nil)
	PingCheckNotifier PingCheckNotifier // nil-safe (handler skips if nil)
	WakeNotifier      WakeNotifier      // nil-safe (handler skips if nil or user is static)
	UI                UIConfig
	Thresholds        state.Thresholds
	// ShutdownCtx, when non-nil, parents the relay goroutines spawned by
	// cmdResultHandler so SIGTERM cancels in-flight TG sends instead of letting
	// them outlive srv.Shutdown (BUG-15). Nil falls back to context.Background
	// for callers that don't wire it.
	ShutdownCtx context.Context
	// ReportRatePerSec / ReportBurst configure the per-token /v1/report rate
	// limit (API-06). Zero disables. Recommended: 0.2 r/s + burst 5 = "1 every
	// 5s sustained, burst of 5". /v1/cmd is naturally long-poll-bounded so we
	// limit only the write path.
	ReportRatePerSec float64
	ReportBurst      int
	// WizardToken enables /v1/wizard/* endpoints when non-empty. Set from
	// cfg.Wizard.Token by main. Empty → endpoints not registered (fail-closed).
	WizardToken string
}

func NewMux(d Deps) http.Handler {
	mux := http.NewServeMux()
	// /healthz: liveness only — process is up & accepting HTTP. Caddy uses
	// it for upstream health, no DB checks. JSON envelope so the deploy
	// wizard can `curl /healthz | jq .version` without SSH (API-12).
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}{Status: "ok", Version: serverVersion})
	})
	// /debug/vars exposes expvar counters (OBS-13). Bound to loopback via
	// cfg.Listen so external attackers can't hit it; if you ever expose
	// backend behind something other than Caddy reverse-proxy, gate this
	// behind a separate mux on a different port.
	mux.Handle("/debug/vars", expvar.Handler())
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
	limiter := newUserRateLimiter(d.ReportRatePerSec, d.ReportBurst)
	rate := RateLimitMiddleware(limiter, d.Logger)
	// /v1/report is the write-path that hits the SQLite writer-mutex; cap
	// its throughput per-token. /v1/cmd long-poll is naturally bounded; result
	// post is small and infrequent (one per cmd) — leave un-limited.
	reqID := requestIDMiddleware()
	mux.Handle("/v1/report", reqID(auth(rate(http.HandlerFunc(reportHandler(d))))))
	if d.CommandSink != nil {
		mux.Handle("/v1/cmd", reqID(auth(http.HandlerFunc(cmdGetHandler(d)))))
		mux.Handle("/v1/cmd/result", reqID(auth(http.HandlerFunc(cmdResultHandler(d)))))
	}
	// Wizard sync endpoints — feature-flagged on cfg.Wizard.Token being non-empty.
	// Single global token, separate from per-agent tokens. Pattern uses Go 1.22+
	// {nickname} path variable on the PUT.
	if d.WizardToken != "" {
		wizAuth := WizardAuthMiddleware(d.WizardToken, d.Logger)
		mux.Handle("GET /v1/wizard/agents", reqID(wizAuth(wizardListAgentsHandler(d))))
		mux.Handle("PUT /v1/wizard/agents/{nickname}", reqID(wizAuth(wizardPutAgentHandler(d))))
		mux.Handle("POST /v1/wizard/agents/{nickname}/deploy", reqID(wizAuth(wizardDeployHandler(d))))
		mux.Handle("GET /v1/wizard/cmd/{cmd_id}", reqID(wizAuth(wizardCmdResultHandler(d))))
	}
	return mux
}

func reportHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxReportBytes+1))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "read body: "+err.Error())
			return
		}
		if len(body) > maxReportBytes {
			writeJSONError(w, http.StatusRequestEntityTooLarge, errCodeBodyTooLarge, "body too large")
			return
		}
		var rep wire.Report
		if err := json.Unmarshal(body, &rep); err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json")
			return
		}
		uid := UserIDFromContext(r.Context())
		nick := NicknameFromContext(r.Context())

		// Resumed=true means the agent self-detected a gap (mobile rejoin).
		// Tell the watcher BEFORE running the FSM so a near-simultaneous
		// scan-tick won't fire a spurious OFFLINE while we ingest the
		// freshly-collected checks.
		if rep.Resumed {
			if d.Resumer != nil {
				d.Resumer.MarkResumed(uid)
			}
			if d.WakeNotifier != nil {
				if u, err := d.DB.Users().GetByID(uid); err == nil && u != nil && u.IsMobile() {
					checks := append([]wire.Check(nil), rep.Checks...)
					nickname := nick
					go func() {
						bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						if err := d.WakeNotifier.SendWake(bg, uid, nickname, checks); err != nil {
							d.Logger.Warn("wake notifier", "nickname", nickname, "err", err)
						}
					}()
				}
			}
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
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "tx begin")
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			"UPDATE users SET last_seen_at = ? WHERE id = ?", ts, uid); err != nil {
			tx.Rollback()
			d.Logger.Warn("update last_seen", "nickname", nick, "err", err)
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "update last_seen")
			return
		}
		// INSERT OR IGNORE — idempotent on (user_id, check_name, ts) per
		// uq_events_user_check_ts (API-01). Agent retry on transient TCP error
		// no longer duplicates the report's events.
		insertStmt, err := tx.PrepareContext(r.Context(),
			`INSERT OR IGNORE INTO events(user_id, check_name, status, details_json, ts) VALUES(?,?,?,?,?)`)
		if err != nil {
			tx.Rollback()
			d.Logger.Warn("report tx prepare", "nickname", nick, "err", err)
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "prepare")
			return
		}
		var dupes int
		for _, c := range rep.Checks {
			detailsJSON := normaliseDetailsJSON(c.Details)
			res, err := insertStmt.ExecContext(r.Context(), uid, c.Name, c.Status, detailsJSON, ts)
			if err != nil {
				insertStmt.Close()
				tx.Rollback()
				d.Logger.Warn("event insert (tx rollback)", "nickname", nick, "check", c.Name, "err", err)
				writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "event insert")
				return
			}
			if n, _ := res.RowsAffected(); n == 0 {
				dupes++
			}
		}
		insertStmt.Close()
		if dupes > 0 {
			addReportDup(dupes)
			d.Logger.Info("report idempotent retry",
				"nickname", nick, "duplicates", dupes, "total", len(rep.Checks),
				"req_id", RequestIDFromContext(r.Context()),
			)
		}
		incReports()
		if err := tx.Commit(); err != nil {
			d.Logger.Warn("report tx commit", "nickname", nick, "err", err)
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "commit")
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
		// OBS-14: full check-summary INFO sampled to 1-in-10 reports + every
		// resumed marker. Per-check status changes already emit dedicated
		// FSM-transition logs (OBS-09); spamming Info every 60s for every
		// agent doubled journal volume without adding signal. Debug carries
		// the full payload for op-on-call who flips log_level=debug.
		anyFail := false
		for _, c := range rep.Checks {
			if c.Status == "fail" {
				anyFail = true
				break
			}
		}
		if anyFail || rep.Resumed || globalReportTracker.shouldLogReport() {
			d.Logger.Info("report",
				"nickname", nick, "agent_version", rep.AgentVersion,
				"resumed", rep.Resumed,
				"check_count", len(rep.Checks), "summary", checksSummaryRollup(rep.Checks),
				"req_id", RequestIDFromContext(r.Context()),
			)
		} else {
			d.Logger.Debug("report",
				"nickname", nick, "agent_version", rep.AgentVersion,
				"check_count", len(rep.Checks), "checks", checkSummary(rep.Checks),
				"req_id", RequestIDFromContext(r.Context()),
			)
		}
		// Pull-based self_update needs the agent's reported version visible
		// to the wizard so it can poll for the post-swap flip. The UPDATE is
		// guarded by a value-changed predicate so the common case (steady
		// state, same version every minute) skips the writer-mutex hit.
		if rep.AgentVersion != "" {
			if err := d.DB.Users().UpdateLastSeenAgentVersion(uid, rep.AgentVersion); err != nil {
				d.Logger.Warn("update last_deployed_version from heartbeat", "nickname", nick, "err", err)
			}
		}
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

// checksSummaryRollup compresses N "name=status" lines into one "Aok/Bfail"
// counter for the sampled INFO log (OBS-14). Cheap to grep / dashboard.
func checksSummaryRollup(checks []wire.Check) string {
	ok, fail, other := 0, 0, 0
	for _, c := range checks {
		switch c.Status {
		case "ok":
			ok++
		case "fail":
			fail++
		default:
			other++
		}
	}
	out := strconv.Itoa(ok) + "ok"
	if fail > 0 {
		out += "/" + strconv.Itoa(fail) + "fail"
	}
	if other > 0 {
		out += "/" + strconv.Itoa(other) + "other"
	}
	return out
}

// cmdGetHandler implements long-poll dequeue. ?wait=N caps the hold window
// (seconds, default 30, max 60). 200+JSON when a command is ready, 204 on
// timeout. Auth context provides the userID — agents only see their own queue.
func cmdGetHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		wait := defaultCmdWait
		if v := r.URL.Query().Get("wait"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeJSONError(w, http.StatusBadRequest, errCodeInvalidWait, "bad wait")
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
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "internal")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		incCmdEnqueued()
		d.Logger.Info("cmd dispatched",
			"nickname", nick, "cmd_id", c.ID, "action", c.Action,
			"req_id", RequestIDFromContext(r.Context()),
		)
	}
}

func cmdResultHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, errCodeMethodNotAll, "method not allowed")
			return
		}
		if !requireJSONContentType(w, r) {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxResultBytes+1))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "read body: "+err.Error())
			return
		}
		if len(body) > maxResultBytes {
			writeJSONError(w, http.StatusRequestEntityTooLarge, errCodeBodyTooLarge, "body too large")
			return
		}
		var res wire.CommandResult
		if err := json.Unmarshal(body, &res); err != nil {
			writeJSONError(w, http.StatusBadRequest, errCodeBadJSON, "bad json")
			return
		}
		if res.ID == "" {
			// 422: parsed correctly but required field is empty (API-03).
			writeJSONError(w, http.StatusUnprocessableEntity, errCodeIDRequired, "id required")
			return
		}
		// Status: log-and-accept для unknown значений. Раньше backend
		// возвращал 400 на любой статус не из whitelist'а — это ломало
		// schema evolution: новый агент с дополнительным статусом ("partial",
		// "rate_limited", и т.п.) терял результат, пока backend не обновлён.
		// Postel's law для внутреннего API: liberal in what we accept.
		if res.Status == "" {
			writeJSONError(w, http.StatusUnprocessableEntity, errCodeStatusReq, "status required")
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
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "record failed")
			return
		}
		incCmdResult()
		// Relay result back to TG (or routes notifier) if a notifier is configured
		// and we recorded the originating message. Async — must not stall the
		// agent's POST on TG network latency.
		if ref, ok := d.CommandSink.ConsumeOriginRef(uid, res.ID); ok {
			incCmdResultRelay()
			switch ref.Action {
			case "route_status", "route_rebind":
				if d.RoutesNotifier != nil {
					go func(ref cmdpkg.MessageRef, res wire.CommandResult) {
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.RoutesNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
							incTGError()
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
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.MaintNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
							incTGError()
							d.Logger.Warn("maint notifier failed", "cmd_id", res.ID, "action", ref.Action, "err", err)
						}
					}(ref, res)
				} else {
					d.Logger.Warn("maint notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			case "pingcheck_status", "pingcheck_toggle":
				if d.PingCheckNotifier != nil {
					go func(ref cmdpkg.MessageRef, res wire.CommandResult) {
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.PingCheckNotifier.NotifyCommandResult(ctx, ref, res, uid); err != nil {
							incTGError()
							d.Logger.Warn("pingcheck notifier failed", "cmd_id", res.ID, "action", ref.Action, "err", err)
						}
					}(ref, res)
				} else {
					d.Logger.Warn("pingcheck notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			case "opkg_upgrade", "opkg_feed_disable":
				if d.OpkgNotifier != nil {
					maxChars := d.UI.DiagMaxChars
					if maxChars == 0 {
						maxChars = 3500
					}
					go func(ref cmdpkg.MessageRef, res wire.CommandResult, maxChars int) {
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.OpkgNotifier.NotifyOpkgResult(ctx, ref, res, uid, maxChars); err != nil {
							incTGError()
							d.Logger.Warn("opkg notifier failed", "cmd_id", res.ID, "action", ref.Action, "err", err)
						}
					}(ref, res, maxChars)
				} else if d.TGNotifier != nil {
					// Graceful fallback: no repair buttons, but still relay the text.
					maxChars := d.UI.DiagMaxChars
					if maxChars == 0 {
						maxChars = 3500
					}
					go func(ref cmdpkg.MessageRef, res wire.CommandResult, uid int64, maxChars int) {
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, uid, maxChars); err != nil {
							incTGError()
							d.Logger.Warn("tg notify failed (opkg fallback)", "cmd_id", res.ID, "err", err)
						}
					}(ref, res, uid, maxChars)
				} else {
					d.Logger.Warn("opkg notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			default:
				if d.TGNotifier != nil {
					maxChars := d.UI.DiagMaxChars
					if maxChars == 0 {
						maxChars = 3500
					}
					go func(ref cmdpkg.MessageRef, res wire.CommandResult, uid int64, maxChars int) {
						ctx, cancel := context.WithTimeout(relayParent(d), 30*time.Second)
						defer cancel()
						if err := d.TGNotifier.NotifyCommandResult(ctx, ref, ref.Action, res, uid, maxChars); err != nil {
							incTGError()
							d.Logger.Warn("tg notify failed", "cmd_id", res.ID, "err", err)
						}
					}(ref, res, uid, maxChars)
				} else {
					d.Logger.Warn("tg notifier not configured; result not relayed",
						"cmd_id", res.ID, "action", ref.Action, "nickname", nick)
				}
			}
		}
		d.Logger.Info("cmd result",
			"nickname", nick, "cmd_id", res.ID, "status", res.Status,
			"duration_ms", res.DurationMs,
			"req_id", RequestIDFromContext(r.Context()),
		)
		w.WriteHeader(http.StatusOK)
	}
}
