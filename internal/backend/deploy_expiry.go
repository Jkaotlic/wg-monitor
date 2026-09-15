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

// AttachDeployExpiryHandler наблюдает команды обновления агента, выброшенные
// из очереди до выдачи: протухшие в Dequeue и вытесненные новой постановкой.
//
// Отметку «обновление назначено» (users.pending_version) здесь НЕ снимаем.
// Намерение живёт в базе, команда в очереди -- одноразовый носитель. Раньше
// снятие здесь теряло обновление выключенному роутеру молча: включившись, он
// первым делом опрашивал команды, натыкался на протухшую, и отметка исчезала
// раньше, чем её увидел отчёт (bronya, gachimikhail: 11.09 -> 15.09.2026).
// Новую команду положит досылка на контакте (deploy_wake.go).
func AttachDeployExpiryHandler(q expiredCommandHandlerSetter, d *db.DB, logger *slog.Logger) {
	if q == nil || d == nil {
		return
	}
	q.SetExpiredCommandHandler(func(userID int64, cmd wire.Command) {
		if cmd.Action != "self_update" || logger == nil {
			return
		}
		logger.Info("self_update dropped from queue; pending deploy kept",
			"user_id", userID, "cmd_id", cmd.ID, "target_version", commandVersionArg(cmd))
	})
}
