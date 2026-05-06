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

// ----- CommandAction (covers all 5 command-channel actions) -----

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
	// When invoked from a real callback, record MessageRef so the result can
	// reply to the original alert. Tests pass q=nil and use bare Enqueue.
	if q != nil {
		ref := cmdpkg.MessageRef{
			ChatID:    q.Message.Chat.ID,
			MessageID: q.Message.MessageID,
			ThreadID:  q.Message.MessageThreadID,
		}
		if err := a.sink.EnqueueWithRef(args.UserID, cmd, ref); err != nil {
			return "", fmt.Errorf("enqueue %s: %w", args.Action, err)
		}
	} else {
		if err := a.sink.Enqueue(args.UserID, cmd); err != nil {
			return "", fmt.Errorf("enqueue %s: %w", args.Action, err)
		}
	}
	return formatQueuedStatus(args.Action), nil
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
	"opkg_upgrade":   "⬆ Opkg upgrade",
	"tunnel_enable":  "▶ Включить",
	"tunnel_disable": "⏸ Выключить",
}
