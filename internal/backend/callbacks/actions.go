package callbacks

import (
	"context"
	"fmt"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

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
