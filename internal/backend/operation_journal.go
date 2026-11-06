package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type miniappOperationsResp struct {
	Operations []publicOperation `json:"operations"`
	NextCursor int64             `json:"next_cursor,omitempty"`
}

// publicOperation is deliberately narrower than db.Operation. The endpoint is
// router-scoped, so actor/request/command identifiers add no operator value and
// would expose Telegram and internal correlation IDs to every allowed viewer.
type publicOperation struct {
	Seq        int64      `json:"seq"`
	Action     string     `json:"action"`
	Source     string     `json:"source"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Outcome    string     `json:"outcome"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Summary    string     `json:"summary,omitempty"`
}

func miniappOperationsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		if !ok || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		limit := 30
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		var before int64
		if raw := r.URL.Query().Get("before"); raw != "" {
			before, _ = strconv.ParseInt(raw, 10, 64)
		}
		ops, err := d.DB.Operations().List(routerID, limit, before)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "operations lookup failed")
			return
		}
		public := make([]publicOperation, 0, len(ops))
		for _, op := range ops {
			public = append(public, publicOperation{
				Seq: op.Seq, Action: op.Action, Source: op.Source, StartedAt: op.StartedAt,
				FinishedAt: op.FinishedAt, Outcome: op.Outcome, ReasonCode: op.ReasonCode, Summary: op.Summary,
			})
		}
		resp := miniappOperationsResp{Operations: public}
		if len(ops) == limit {
			resp.NextCursor = ops[len(ops)-1].Seq
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// operationSummary is intentionally an allowlist. Command args may contain
// endpoints, credentials or topology and are never interpolated into the
// operator-visible journal.
func operationSummary(action string, _ map[string]any) string {
	switch action {
	case "tunnel_restart":
		return "Перезапуск VPN-туннеля"
	case "route_policy_promote":
		return "Переключение трафика на другой VPN-туннель"
	case "tunnel_import":
		return "Добавление VPN-туннеля"
	case "tunnel_delete":
		return "Удаление VPN-туннеля"
	case "tunnel_power", "tunnel_enable", "tunnel_disable":
		return "Изменение состояния VPN-туннеля"
	case "agent_deploy", "agent_update":
		return "Обновление агента"
	default:
		return "Операция на роутере"
	}
}

func BeginOperation(ctx context.Context, database *db.DB, routerID, actorID int64, action, source string, args map[string]any) (string, error) {
	id := "op_" + newRequestID()
	err := database.Operations().Begin(db.Operation{
		ID: id, RequestID: RequestIDFromContext(ctx), RouterID: routerID, ActorID: actorID,
		Action: action, Source: source, StartedAt: time.Now().UTC(), Summary: operationSummary(action, args),
	})
	return id, err
}

func AttachOperationCommand(database *db.DB, operationID, commandID string) error {
	return database.Operations().AttachCommand(operationID, commandID)
}

func FinishOperation(database *db.DB, operationID, outcome, reasonCode string, at time.Time) error {
	return database.Operations().Finish(operationID, db.OperationFinish{FinishedAt: at.UTC(), Outcome: outcome, ReasonCode: reasonCode})
}

func FinishOperationByCommand(database *db.DB, commandID, outcome, reasonCode string, at time.Time) error {
	return database.Operations().FinishByCommand(commandID, db.OperationFinish{FinishedAt: at.UTC(), Outcome: outcome, ReasonCode: reasonCode})
}

func finishCommandOperation(database *db.DB, commandID, status string, at time.Time) error {
	outcome, reason := "failed", "command_failed"
	switch status {
	case "ok":
		outcome, reason = "ok", "command_completed"
	case "timeout":
		reason = "command_timeout"
	case "locked":
		reason = "command_locked"
	}
	return FinishOperationByCommand(database, commandID, outcome, reason, at)
}
