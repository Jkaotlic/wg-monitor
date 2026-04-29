package callbacks

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

// TGClient is the subset of tg.Client used by the router.
type TGClient interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
	AnswerCallbackQuery(ctx context.Context, callbackID, text string) error
	EditMessageText(ctx context.Context, chatID, messageID int64, text, parseMode string, markup *tg.InlineKeyboardMarkup) error
	GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tg.Update, error)
}

type Config struct {
	ChatID         int64
	AdminUserID    int64
	MuteCutoffHour int
}

type Router struct {
	d       *db.DB
	tg      TGClient
	cfg     Config
	silence *SilenceAction
	ack     *AckAction
	mute    *MuteAction
	history *HistoryAction
	command *CommandAction
}

// NewRouter builds a Router without a command-channel sink. Command-action
// callbacks (restart_tunnel/diag_now/...) will toast an error.
func NewRouter(d *db.DB, tgClient TGClient, cfg Config) *Router {
	return NewRouterWithSink(d, tgClient, nil, cfg)
}

// NewRouterWithSink builds a Router whose command-action callbacks enqueue
// wire.Command into the provided sink for the agent to long-poll.
func NewRouterWithSink(d *db.DB, tgClient TGClient, sink CommandEnqueuer, cfg Config) *Router {
	return &Router{
		d:       d,
		tg:      tgClient,
		cfg:     cfg,
		silence: NewSilenceAction(d),
		ack:     NewAckAction(d),
		mute:    NewMuteAction(d, cfg.MuteCutoffHour),
		history: NewHistoryAction(d, tgClient, cfg.ChatID),
		command: NewCommandAction(sink, nil),
	}
}

// Run loops on GetUpdates, persisting the last-processed update_id in tg_state KV.
// Backoff on errors. Exits when ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	var attempt int
	offset, _ := r.loadOffset()
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := r.tg.GetUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			attempt++
			wait := time.Duration(math.Min(math.Pow(2, float64(attempt)), 60)) * time.Second
			slog.Warn("getUpdates failed; backoff", "err", err, "wait", wait)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
			continue
		}
		attempt = 0
		for _, u := range updates {
			r.handleUpdate(ctx, u)
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
		}
		if len(updates) > 0 {
			_ = r.saveOffset(offset)
		}
	}
}

func (r *Router) handleUpdate(ctx context.Context, u tg.Update) {
	if u.CallbackQuery == nil {
		return
	}
	r.HandleCallback(ctx, u.CallbackQuery)
}

func (r *Router) loadOffset() (int64, error) {
	s, err := r.d.KV().Get("last_update_id")
	if err != nil || s == "" {
		return 0, err
	}
	return strconv.ParseInt(s, 10, 64)
}

func (r *Router) saveOffset(offset int64) error {
	return r.d.KV().Set("last_update_id", strconv.FormatInt(offset, 10))
}

// HandleCallback applies allowlist, parses, dispatches to action, edits message.
// Exposed for tests.
func (r *Router) HandleCallback(ctx context.Context, q *tg.CallbackQuery) {
	if q.From.ID != r.cfg.AdminUserID {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "not authorized")
		slog.Warn("rejected callback (allowlist)", "from", q.From.ID, "data", q.Data)
		return
	}
	args, err := Parse(q.Data)
	if err != nil {
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "unknown action")
		slog.Warn("malformed callback_data", "data", q.Data, "err", err)
		return
	}
	// Parse() validates args.Action against the action whitelist (parse.go validActions).
	var action Action
	switch args.Action {
	case "silence":
		action = r.silence
	case "ack":
		action = r.ack
	case "mute":
		action = r.mute
	case "history":
		action = r.history
	case "restart_tunnel", "diag_now", "pingcheck_now", "force_recheck", "opkg_upgrade":
		action = r.command
	}
	statusLine, err := action.Apply(ctx, q, args)
	if err != nil {
		msg := "error: " + err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		_ = r.tg.AnswerCallbackQuery(ctx, q.ID, msg)
		slog.Error("action failed", "action", args.Action, "err", err)
		return
	}
	_ = r.tg.AnswerCallbackQuery(ctx, q.ID, "")
	if statusLine == "" {
		// History returns "" — do not edit original.
		return
	}
	newText := q.Message.Text + "\n\n" + statusLine
	empty := tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{}}
	if err := r.tg.EditMessageText(ctx, q.Message.Chat.ID, q.Message.MessageID, newText, "", &empty); err != nil {
		slog.Warn("editMessageText failed (state already updated)", "err", err)
	}
}
