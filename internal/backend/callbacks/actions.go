package callbacks

import (
	"context"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alertaction"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// SilenceAction -- «⏸ Тише на час» под уведомлением.
type SilenceAction struct{ d *db.DB }

func NewSilenceAction(d *db.DB) *SilenceAction { return &SilenceAction{d: d} }

// role -- кто нажал: alertaction.RoleAdmin или роль db.RouterAccessRole.
func (a *SilenceAction) Apply(_ context.Context, _ *tg.CallbackQuery, args Args, role string) (string, error) {
	st, err := a.d.State().Get(args.UserID, args.CheckName)
	if err != nil {
		return "", err
	}
	st, line := alertaction.ApplySilence(st, args.TTL, time.Now(), role)
	if err := a.d.State().Save(args.UserID, args.CheckName, st); err != nil {
		return "", err
	}
	return line, nil
}
