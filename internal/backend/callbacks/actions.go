package callbacks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// CommandEnqueuer is the subset of cmd.Queue used by CommandAction. Decoupled
// from the concrete queue so router_test/actions_test can substitute fakes.
type CommandEnqueuer interface {
	Enqueue(userID int64, cmd wire.Command) error
	EnqueueWithRef(userID int64, cmd wire.Command, ref cmdpkg.MessageRef) error
}

// Action applies a callback to incident state and returns a status line
// to append to the original message after the keyboard is removed.
// q is the original CallbackQuery (nil OK in unit tests that bypass TG).
type Action interface {
	Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (statusLine string, err error)
}

// ----- Silence -----

type SilenceAction struct{ d *db.DB }

func NewSilenceAction(d *db.DB) *SilenceAction { return &SilenceAction{d: d} }

func (a *SilenceAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	st, err := a.d.State().Get(args.UserID, args.CheckName)
	if err != nil {
		return "", err
	}
	until := time.Now().Add(args.TTL)
	st.SilencedUntil = &until
	if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
		return "", err
	}
	return fmt.Sprintf("⏸ Silenced до %s МСК (admin)",
		until.In(moscowLoc()).Format("15:04")), nil
}

func moscowLoc() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*3600)
	}
	return loc
}

// ----- Ack -----

type AckAction struct{ d *db.DB }

func NewAckAction(d *db.DB) *AckAction { return &AckAction{d: d} }

func (a *AckAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	st, err := a.d.State().Get(args.UserID, args.CheckName)
	if err != nil {
		return "", err
	}
	st.Acked = true
	if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
		return "", err
	}
	return "✅ Ack'ed (до восстановления)", nil
}

// ----- Mute -----

type MuteAction struct {
	d          *db.DB
	cutoffHour int
}

func NewMuteAction(d *db.DB, cutoffHour int) *MuteAction {
	return &MuteAction{d: d, cutoffHour: cutoffHour}
}

func (a *MuteAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	loc := moscowLoc()
	until := nextCutoff(time.Now().In(loc), a.cutoffHour, loc)
	st, err := a.d.State().Get(args.UserID, args.CheckName)
	if err != nil {
		return "", err
	}
	untilUTC := until.UTC()
	st.SilencedUntil = &untilUTC
	if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
		return "", err
	}
	return fmt.Sprintf("🔇 Muted до %02d:00 МСК (%s)",
		a.cutoffHour, until.Format("02 Jan")), nil
}

// nextCutoff returns the next moment of `hour:00` in `loc`, strictly in the future.
// If now is exactly at hour:00, returns next day's hour:00.
func nextCutoff(now time.Time, hour int, loc *time.Location) time.Time {
	nowLoc := now.In(loc)
	target := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), hour, 0, 0, 0, loc)
	if !target.After(nowLoc) {
		target = target.AddDate(0, 0, 1)
	}
	return target
}

// ----- History -----

type historyTG interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type HistoryAction struct {
	d      *db.DB
	tg     historyTG
	chatID int64
}

func NewHistoryAction(d *db.DB, tgClient historyTG, chatID int64) *HistoryAction {
	return &HistoryAction{d: d, tg: tgClient, chatID: chatID}
}

const historyMaxTransitions = 30

func (a *HistoryAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	cutoff := time.Now().Add(-24 * time.Hour)
	events, err := a.d.Events().ListSince(args.UserID, args.CheckName, cutoff)
	if err != nil {
		return "", err
	}

	// Compute transitions: only emit lines on status change.
	var lines []string
	var prev string
	for _, e := range events {
		if e.Status != prev {
			icon := "✅"
			if e.Status == "fail" {
				icon = "❌"
			}
			lines = append(lines, fmt.Sprintf("%s %s %s", e.TS.In(moscowLoc()).Format("15:04"), icon, e.Status))
			prev = e.Status
		}
	}

	// Look up nickname and thread.
	user, err := a.d.Users().GetByID(args.UserID)
	if err != nil {
		return "", fmt.Errorf("history: lookup user %d: %w", args.UserID, err)
	}
	var nickname string
	var threadID *int64
	if user != nil {
		nickname = user.Nickname
		threadID = user.TelegramThreadID
	}
	header := fmt.Sprintf("📋 История [%s] / %s — 24ч", nickname, args.CheckName)

	var body string
	if len(lines) == 0 {
		body = "(нет событий за период)"
	} else {
		truncated := false
		if len(lines) > historyMaxTransitions {
			lines = lines[len(lines)-historyMaxTransitions:]
			truncated = true
		}
		body = strings.Join(lines, "\n")
		if truncated {
			body = "... (older entries truncated)\n" + body
		}
	}
	text := header + "\n" + body

	_, err = a.tg.SendMessage(ctx, a.chatID, threadID, text, "", nil)
	if err != nil {
		return "", err
	}
	// History does NOT edit original — return empty status line so router skips edit.
	return "", nil
}

// ----- CommandAction (covers command-channel actions) -----

type CommandAction struct {
	sink  CommandEnqueuer
	idGen func() string
}

// NewCommandAction wires up a CommandAction. idGen is injectable so tests
// can pin a deterministic ID; pass nil for the default (16-byte hex random).
func NewCommandAction(sink CommandEnqueuer, idGen func() string) *CommandAction {
	if idGen == nil {
		idGen = defaultCmdID
	}
	return &CommandAction{sink: sink, idGen: idGen}
}

func defaultCmdID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *CommandAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	if a.sink == nil {
		return "", errors.New("command channel disabled (no sink configured)")
	}
	cmdArgs := map[string]any{"check_name": args.CheckName}
	if args.NDMSName != "" {
		cmdArgs["ndms_name"] = args.NDMSName
	}
	cmd := wire.Command{
		ID:       a.idGen(),
		Action:   args.Action,
		Args:     cmdArgs,
		IssuedAt: time.Now().UTC(),
	}
	// Always go through EnqueueWithRef so the result-relay codepath is the
	// single source of truth. Tests previously passed q==nil and went through
	// the bare-Enqueue branch, but that diverged production from tests; the
	// nil-ref case is harmless (zero ChatID/MessageID skips the relay).
	var ref cmdpkg.MessageRef
	if q != nil {
		ref = cmdpkg.MessageRef{
			ChatID:    q.Message.Chat.ID,
			MessageID: q.Message.MessageID,
			ThreadID:  q.Message.MessageThreadID,
		}
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue %s: %w", args.Action, err)
	}
	return formatQueuedStatus(args.Action), nil
}

// DispatchFromMessage enqueues a command originating from a *text* message
// (not an inline-button callback). Used by the router for ReplyKeyboard
// taps like "🌍 Через тоннель?" / "🇷🇺 Напрямую?". The agent's
// CommandResult will reply to the user's text message via Notifier.
func (a *CommandAction) DispatchFromMessage(_ context.Context, action string, userID int64, chatID, messageID int64, threadID *int64) error {
	if a.sink == nil {
		return errors.New("command channel disabled (no sink configured)")
	}
	cmd := wire.Command{
		ID:       a.idGen(),
		Action:   action,
		Args:     map[string]any{},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    chatID,
		MessageID: messageID,
		ThreadID:  threadID,
		Action:    action,
	}
	return a.sink.EnqueueWithRef(userID, cmd, ref)
}

// formatQueuedStatus is the user-facing label appended to the alert message
// after a command-channel button is tapped. Plain action names map to icons.
func formatQueuedStatus(action string) string {
	label := commandLabels[action]
	if label == "" {
		label = action
	}
	return fmt.Sprintf("📤 %s поставлено в очередь", label)
}

var commandLabels = map[string]string{
	"restart_tunnel": "🔁 Restart",
	"diag_now":       "📊 Diag",
	"pingcheck_now":  "▶ Pingcheck",
	"force_recheck":  "🔁 Force recheck",
	"router_doctor":  "🩺 Проверка",
	"opkg_upgrade":   "⬆ Opkg upgrade",
	"tunnel_enable":  "▶ Включить",
	"tunnel_disable": "⏸ Выключить",
}

// pendingRebind holds a scheduled rebind awaiting Confirm. Stored in
// Router.pendingRebinds keyed by token; consumed by RebindConfirmAction.
// Mirrors pendingUpload in lifetime semantics: token+TTL, single use.
type pendingRebind struct {
	UserID    int64
	SrcID     string
	DstID     string
	Token     string
	ExpiresAt time.Time
}

// RebindConfirmAction handles routes_confirm:<uid>:<src>:<dst>:<token>. It
// consumes the pendingRebind by token and enqueues a route_rebind wire.Command.
type RebindConfirmAction struct {
	sink      CommandEnqueuer
	consumeFn func(userID int64, token string) (*pendingRebind, bool)
	idGen     func() string
}

func NewRebindConfirmAction(sink CommandEnqueuer, consume func(int64, string) (*pendingRebind, bool), idGen func() string) *RebindConfirmAction {
	return &RebindConfirmAction{sink: sink, consumeFn: consume, idGen: idGen}
}

func (a *RebindConfirmAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	pr, ok := a.consumeFn(args.UserID, args.RebindToken)
	if !ok {
		return "", errors.New("сессия истекла или не найдена; открой панель заново")
	}
	if pr.SrcID != args.RebindSrcID || pr.DstID != args.RebindDstID {
		return "", errors.New("параметры rebind не совпадают с подтверждением")
	}
	cmd := wire.Command{
		ID:     a.idGen(),
		Action: "route_rebind",
		Args: map[string]any{
			"src_tunnel_id": pr.SrcID,
			"dst_tunnel_id": pr.DstID,
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue route_rebind: %w", err)
	}
	return "🛣 запускаем перенос…", nil
}

// makeRebindToken returns 8 hex chars cryptographically random.
func makeRebindToken() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// pendingUpload holds a downloaded .conf while the admin confirms what to do.
// Stored in Router.pending keyed by userID; consumed by ImportAction.
type pendingUpload struct {
	ConfB64       string
	Name          string // empty = still waiting for admin to type tunnel name
	SuggestedName string // sanitized from filename, shown in "how to name?" prompt
	ThreadID      *int64
	Token         string    // 8-hex random, embedded in callback_data
	ExpiresAt     time.Time // 5 min from upload
}

// ImportAction handles tunnel_import_replace / tunnel_import_add callback buttons.
// It looks up the pending conf upload, then enqueues a tunnel_import wire.Command.
type ImportAction struct {
	sink      CommandEnqueuer
	consumeFn func(userID int64, token string) (*pendingUpload, bool)
	idGen     func() string
}

func (a *ImportAction) Apply(ctx context.Context, q *tg.CallbackQuery, args Args) (string, error) {
	up, ok := a.consumeFn(args.UserID, args.ImportToken)
	if !ok {
		return "", fmt.Errorf("загрузка истекла или не найдена; отправь конфиг заново")
	}
	replace := args.Action == "tunnel_import_replace"
	cmd := wire.Command{
		ID:     a.idGen(),
		Action: "tunnel_import",
		Args: map[string]any{
			"conf":    up.ConfB64,
			"name":    up.Name,
			"replace": replace,
		},
		IssuedAt: time.Now().UTC(),
	}
	ref := cmdpkg.MessageRef{
		ChatID:    q.Message.Chat.ID,
		MessageID: q.Message.MessageID,
		ThreadID:  q.Message.MessageThreadID,
	}
	if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
		return "", fmt.Errorf("enqueue tunnel_import: %w", err)
	}
	verb := "добавление"
	if replace {
		verb = "замена"
	}
	return fmt.Sprintf("📤 Import (%s %q) поставлен в очередь", verb, up.Name), nil
}
