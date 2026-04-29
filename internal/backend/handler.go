package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const maxReportBytes = 64 * 1024

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

type Deps struct {
	Logger     *slog.Logger
	DB         *db.DB
	Dispatcher Dispatcher
	Resumer    Resumer
	Thresholds state.Thresholds
}

func NewMux(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	auth := AuthMiddleware(d.DB.Users())
	mux.Handle("/v1/report", auth(http.HandlerFunc(reportHandler(d))))
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

		_ = d.DB.Users().UpdateLastSeen(uid)
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
		for _, c := range rep.Checks {
			detailsJSON, _ := json.Marshal(c.Details)
			if err := d.DB.Events().Insert(uid, c.Name, c.Status, string(detailsJSON), ts); err != nil {
				d.Logger.Warn("event insert", "nickname", nick, "check", c.Name, "err", err)
				continue
			}
			if c.Name == "agent_heartbeat" {
				continue
			}
			prev, err := d.DB.State().Get(uid, c.Name)
			if err != nil {
				d.Logger.Warn("state.Get", "err", err)
				continue
			}
			tr := state.Apply(prev, c.Status, time.Now(), d.Thresholds)
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
