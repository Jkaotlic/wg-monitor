package backend

import (
	"log/slog"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
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
//
// Параметр *db.DB здесь раньше был (снятие отметки жило в этом обработчике);
// сейчас функция только логирует, базу не трогает -- сигнатура без него (B4).
func AttachDeployExpiryHandler(q expiredCommandHandlerSetter, logger *slog.Logger) {
	if q == nil {
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

// selfUpdateDispatchWindow -- сколько выданная self_update без ответа держит
// место в раздаче. Агенту на всё действие отведено 10 минут
// (internal/agent/actions/runner.go), сверху запас на доставку ответа; дольше
// молчит только агент, ушедший в перезагрузку, и его место пора отдать.
const selfUpdateDispatchWindow = 12 * time.Minute

type dispatchLimitSetter interface {
	SetDispatchLimit(action string, maxInFlight int, window time.Duration)
}

// AttachDeployDispatchLimit размазывает раздачу обновлений агента: self_update
// выдаётся не больше чем maxConcurrentReleaseProxy роутерам разом -- ровно
// столько бинарей прокси релизов раздаёт одновременно (release_proxy.go).
//
// Прод 18.09: «Обновить всех отставших» и досылка при контакте выдавали
// self_update всему парку в одну секунду, и 19 роутеров из 19 упёрлись в 503.
// Теперь отметку получают все сразу, а команда остальных ждёт в очереди:
// роутер на связи опрашивает её каждую минуту, и очередь рассасывается сама.
// Попытку ожидание не тратит -- она засчитывается при выдаче.
func AttachDeployDispatchLimit(q dispatchLimitSetter) {
	if q == nil {
		return
	}
	q.SetDispatchLimit("self_update", maxConcurrentReleaseProxy, selfUpdateDispatchWindow)
}
