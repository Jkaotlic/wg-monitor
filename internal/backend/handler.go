package backend

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/pkg/wire"
)

const maxReportBytes = 64 * 1024

type Dispatcher interface {
	Handle(ctx context.Context, userID int64, nickname, checkName string, tr state.Transition, detail string) error
}

type Deps struct {
	Logger     *slog.Logger
	DB         *db.DB
	Dispatcher Dispatcher
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
			detail := buildDetail(c)
			if err := d.Dispatcher.Handle(r.Context(), uid, nick, c.Name, tr, detail); err != nil {
				d.Logger.Warn("dispatch", "check", c.Name, "kind", tr.Kind, "err", err)
			}
		}
		d.Logger.Info("report",
			"nickname", nick, "agent_version", rep.AgentVersion,
			"check_count", len(rep.Checks), "checks", checkSummary(rep.Checks),
		)
		w.WriteHeader(http.StatusOK)
	}
}

func buildDetail(c wire.Check) string {
	if c.Status == "ok" {
		return ""
	}
	if e, ok := c.Details["error"].(string); ok {
		return e
	}
	b, _ := json.Marshal(c.Details)
	return string(b)
}

func checkSummary(checks []wire.Check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name + "=" + c.Status
	}
	return out
}
