package backend

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alertaction"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// miniappEmptyKeyboard is the explicitly-empty inline keyboard used to strip
// buttons from an alert message. Telegram's editMessageReplyMarkup rejects a
// zero-value InlineKeyboardMarkup{} (which marshals to
// {"inline_keyboard":null}); an explicit empty slice
// ({"inline_keyboard":[]}) is the form the Bot API accepts — the same one
// the callback router's post-action edits already rely on
// (internal/backend/callbacks/router.go).
var miniappEmptyKeyboard = tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}

// miniappIncident is the enriched incident shape exposed by the mini-app:
// includes current suppression state so the UI can render "silenced until X" /
// "acked" instead of blindly offering action buttons.
type miniappIncident struct {
	CheckName     string     `json:"check_name"`
	HardSince     *time.Time `json:"hard_since,omitempty"`
	FailCount     int        `json:"fail_count"`
	SilencedUntil *time.Time `json:"silenced_until,omitempty"`
	Acked         bool       `json:"acked"`
}

func miniappIncidentFromState(st db.IncidentState) miniappIncident {
	return miniappIncident{
		CheckName:     st.CheckName,
		HardSince:     st.HardSince,
		FailCount:     st.ConsecutiveFails,
		SilencedUntil: st.SilencedUntil,
		Acked:         st.Acked,
	}
}

type miniappIncidentResp struct {
	Incident miniappIncident `json:"incident"`
}

var miniappSilenceTTLs = map[string]time.Duration{
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"24h": 24 * time.Hour,
}

// miniappIncidentAction is the shared spine for silence/ack/mute: it resolves +
// access-checks the (router, check), loads the incident state, applies the
// caller-supplied mutation, saves, best-effort-syncs the Telegram message, and
// returns the updated incident. mutate returns the new state + the status line
// used for the breadcrumb.
func miniappIncidentAction(d Deps, w http.ResponseWriter, r *http.Request, mutate func(st db.IncidentState) (db.IncidentState, string)) {
	telegramUserID, _ := miniappUserFromContext(r.Context())
	routerID, ok := parseMiniappRouterID(r)
	check := r.PathValue("check")
	if !ok || check == "" || !miniappRouterAllowed(d, telegramUserID, routerID) {
		writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
		return
	}
	st, err := d.DB.State().Get(routerID, check)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "state lookup failed")
		return
	}
	if st.CurrentStatus != "hard" {
		writeJSONError(w, http.StatusNotFound, "incident_not_active", "incident is no longer active")
		return
	}
	newSt, statusLine := mutate(st)
	if err := d.DB.State().Save(routerID, check, newSt); err != nil {
		writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "state save failed")
		return
	}
	miniappSyncAlertMessage(d, r, routerID, newSt, statusLine)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(miniappIncidentResp{Incident: miniappIncidentFromState(newSt)})
}

// miniappSyncAlertMessage strips the buttons from the original alert and posts
// a threaded breadcrumb — all best-effort. A nil MiniappTG or a missing
// LastAlertMsgID skips it silently; Bot API errors are logged, not surfaced.
func miniappSyncAlertMessage(d Deps, r *http.Request, routerID int64, st db.IncidentState, statusLine string) {
	if d.MiniappTG == nil || st.LastAlertMsgID == nil || statusLine == "" {
		return
	}
	user, err := d.DB.Users().GetByID(routerID)
	if err != nil || user == nil {
		return
	}
	chatID := user.EffectiveTelegramChatID(d.TelegramPrimaryChatID)
	msgID := *st.LastAlertMsgID
	ctx := r.Context()
	if err := d.MiniappTG.EditMessageReplyMarkup(ctx, chatID, msgID, &miniappEmptyKeyboard); err != nil && d.Logger != nil {
		d.Logger.Warn("miniapp: strip alert buttons failed", "router_id", routerID, "err", err)
	}
	breadcrumb := statusLine + " (через приложение)"
	if _, err := d.MiniappTG.SendMessage(ctx, chatID, user.TelegramThreadID, breadcrumb, "", &msgID); err != nil && d.Logger != nil {
		d.Logger.Warn("miniapp: alert breadcrumb failed", "router_id", routerID, "err", err)
	}
}

func miniappSilenceHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			TTL string `json:"ttl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad_request", "invalid body")
			return
		}
		ttl, ok := miniappSilenceTTLs[body.TTL]
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "bad_ttl", "ttl must be 1h, 4h or 24h")
			return
		}
		miniappIncidentAction(d, w, r, func(st db.IncidentState) (db.IncidentState, string) {
			return alertaction.ApplySilence(st, ttl, time.Now())
		})
	}
}

func miniappAckHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		miniappIncidentAction(d, w, r, func(st db.IncidentState) (db.IncidentState, string) {
			return alertaction.ApplyAck(st, time.Now())
		})
	}
}

func miniappMuteHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		miniappIncidentAction(d, w, r, func(st db.IncidentState) (db.IncidentState, string) {
			return alertaction.ApplyMute(st, d.MuteCutoffHour, time.Now())
		})
	}
}

type miniappTransition struct {
	Timestamp string `json:"ts"`     // RFC3339 UTC
	Status    string `json:"status"` // "ok" | "fail"
	Label     string `json:"label"`  // "в норме" | "сбой"
}

type miniappHistoryResp struct {
	Transitions []miniappTransition `json:"transitions"`
	Truncated   bool                `json:"truncated"`
}

// miniappHistoryWindow bounds the history lookback (matches the callback
// History action's 24h window).
const miniappHistoryWindow = 24 * time.Hour

func miniappHistoryLabel(status string) string {
	if status == "fail" {
		return "сбой"
	}
	return "в норме"
}

func miniappHistoryHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		routerID, ok := parseMiniappRouterID(r)
		check := r.PathValue("check")
		if !ok || check == "" || !miniappRouterAllowed(d, telegramUserID, routerID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "router not found")
			return
		}
		events, err := d.DB.Events().ListSince(routerID, check, time.Now().Add(-miniappHistoryWindow))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "history lookup failed")
			return
		}
		transitions, truncated := alertaction.Transitions(events)
		resp := miniappHistoryResp{Truncated: truncated}
		for _, tr := range transitions {
			resp.Transitions = append(resp.Transitions, miniappTransition{
				Timestamp: tr.TS.UTC().Format(time.RFC3339),
				Status:    tr.Status,
				Label:     miniappHistoryLabel(tr.Status),
			})
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
