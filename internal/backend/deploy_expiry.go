package backend

import (
	"log/slog"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type expiredCommandHandlerSetter interface {
	SetExpiredCommandHandler(cmdpkg.ExpiredCommandHandler)
}

func AttachDeployExpiryHandler(q expiredCommandHandlerSetter, d *db.DB, logger *slog.Logger) {
	if q == nil || d == nil {
		return
	}
	q.SetExpiredCommandHandler(func(userID int64, cmd wire.Command) {
		if cmd.Action != "self_update" {
			return
		}
		target := commandVersionArg(cmd)
		if target == "" {
			return
		}
		cleared, err := d.Users().ClearPendingDeployIfMatches(userID, target)
		if err != nil {
			if logger != nil {
				logger.Warn("clear pending deploy after expired self_update",
					"user_id", userID, "cmd_id", cmd.ID, "target_version", target, "err", err)
			}
			return
		}
		if cleared && logger != nil {
			logger.Info("cleared pending deploy after expired self_update",
				"user_id", userID, "cmd_id", cmd.ID, "target_version", target)
		}
	})
}
