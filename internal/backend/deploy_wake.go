// internal/backend/deploy_wake.go
package backend

import (
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// activeCommandChecker -- узкая часть очереди, нужная постановке при
// пробуждении. Отдельный интерфейс с проверкой типа, а не метод в
// CommandSink: у того восемь десятков точек сборки в тестах, и расширение
// интерфейса сломало бы их все ради одной подсказки.
type activeCommandChecker interface {
	HasActiveCommand(userID int64, action string) bool
}

// activeEnqueueChecker -- атомарная «проверить и положить», нужна досылке при
// пробуждении, чтобы закрыть окно между HasActiveCommand и Enqueue (B1).
// Тот же узкий интерфейс-по-типу, что activeCommandChecker: расширять
// CommandSink нельзя по той же причине (восемь десятков сборок в тестах).
type activeEnqueueChecker interface {
	EnqueueIfNoActive(userID int64, cmd wire.Command) (bool, error)
}

// buildSelfUpdateCommand собирает команду обновления агента. Тело одинаковое
// у постановки после рестарта (deploy_resume.go) и у постановки при
// пробуждении, поэтому живёт в одном месте.
func buildSelfUpdateCommand(id, target, publicBaseURL, publicIP string, now time.Time) wire.Command {
	cmd := wire.Command{
		ID:     id,
		Action: "self_update",
		Args: map[string]any{
			"version":   target,
			"repo_base": strings.TrimRight(strings.TrimSpace(publicBaseURL), "/") + "/v1/releases/download",
		},
		IssuedAt: now,
	}
	if ip := strings.TrimSpace(publicIP); ip != "" {
		cmd.Args["repo_resolve_ip"] = ip
	}
	return cmd
}

// ensurePendingDeployQueued -- досылка назначенного обновления, когда роутер
// вышел на связь: перед выдачей команд в GET /v1/cmd и на отчёте (в ветке
// requeue, когда версия агента ещё не совпала с целью).
//
// Зачем перед выдачей: включившийся агент первым делом опрашивает команды и
// только потом отчитывается (cmd/agent/main.go:132 против :141). Досылка
// только на отчёте опаздывала на весь первый опрос.
//
// Намерение оператора хранится в базе, поэтому источником правды остаётся
// она: пока стоит pending_version, каждый контакт без активной команды --
// новая попытка. Двойную выдачу исключает HasActiveCommand: активной
// считается и непротухшая в очереди, и выданная, пока не отжила свой TTL.
//
// Сдачу (give-up) эта функция НЕ решает: при исчерпанных попытках она просто
// не досылает. Опрос вызывает её раньше первого отчёта и не знает версию
// агента -- объявить «не ставится» здесь было бы преждевременно и давало бы
// ложную тревогу для роутера, который на самом деле уже обновился, но ещё не
// отчитался (review Important #1). Решение о сдаче -- только в handler.go на
// отчёте, после того как версия агента доказана (giveUpIfExhausted).
func ensurePendingDeployQueued(d Deps, uid int64, nickname string, now time.Time) {
	base := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
	if d.DB == nil || d.CommandSink == nil || base == "" {
		// Без публичного адреса ссылку на бинарь собрать нечем -- агент по
		// такой команде ничего не скачает.
		return
	}
	st, err := d.DB.Users().PendingDeploy(uid)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on contact: read pending", "nickname", nickname, "err", err)
		}
		return
	}
	if st.Version == "" {
		return
	}
	if pendingDeployExpired(st.Since, now) {
		cleared, err := d.DB.Users().ClearPendingDeploy(uid)
		if d.Logger != nil {
			d.Logger.Info("deploy on contact: pending deploy older than 90 days dropped",
				"nickname", nickname, "target_version", st.Version, "pending_since", st.Since,
				"cleared", cleared, "err", err)
		}
		return
	}
	if st.Attempts >= pendingDeployMaxAttempts {
		// Не сдаёмся здесь -- см. комментарий над функцией. Просто не
		// досылаем; решение примет отчёт, когда докажет версию. Активность всё
		// равно проверяем -- при активной команде и без того нечего логировать
		// каждый контакт молчаливым «ждём отчёт».
		if checker, ok := d.CommandSink.(activeCommandChecker); ok && checker.HasActiveCommand(uid, "self_update") {
			return
		}
		if d.Logger != nil {
			d.Logger.Info("deploy on contact: attempts exhausted; waiting for report to confirm version before giving up",
				"nickname", nickname, "target_version", st.Version, "attempts", st.Attempts)
		}
		return
	}
	enqueuePendingDeployIfNoActive(d, uid, nickname, st.Version, base, now)
}

// enqueuePendingDeployIfNoActive кладёт свежую команду обновления, только
// если для пользователя ещё нет активной self_update. Предпочитает
// атомарную проверку-и-постановку очереди (activeEnqueueChecker): у
// production-очереди (cmd.Queue) HasActiveCommand и Enqueue раздельными
// вызовами оставляли окно двойной выдачи (B1) -- досылка при пробуждении
// (эта функция) и опрос/отчёт, вызывающие её почти одновременно для одного
// и того же роутера, оба видели «не занято» и оба ставили команду. Без
// такого метода у CommandSink (тестовые дублёры) поведение прежнее:
// раздельные проверка и постановка -- тесты этот путь и покрывают.
//
// Прежняя протухшая команда того же действия вытесняется очередью сама
// (queue.go supersede) в обоих путях.
func enqueuePendingDeployIfNoActive(d Deps, uid int64, nickname, target, base string, now time.Time) bool {
	id, err := newCmdID()
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on contact: id gen failed", "nickname", nickname, "err", err)
		}
		return false
	}
	cmd := buildSelfUpdateCommand(id, target, base, d.PublicIP, now)
	if enq, ok := d.CommandSink.(activeEnqueueChecker); ok {
		queued, err := enq.EnqueueIfNoActive(uid, cmd)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("deploy on contact: enqueue failed",
					"nickname", nickname, "target_version", target, "err", err)
			}
			return false
		}
		if !queued {
			return false
		}
		if d.Logger != nil {
			d.Logger.Info("deploy on contact: re-queued self_update",
				"nickname", nickname, "user_id", uid, "target_version", target, "cmd_id", id)
		}
		return true
	}
	if checker, ok := d.CommandSink.(activeCommandChecker); ok && checker.HasActiveCommand(uid, "self_update") {
		return false
	}
	if err := d.CommandSink.Enqueue(uid, cmd); err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on contact: enqueue failed",
				"nickname", nickname, "target_version", target, "err", err)
		}
		return false
	}
	if d.Logger != nil {
		d.Logger.Info("deploy on contact: re-queued self_update",
			"nickname", nickname, "user_id", uid, "target_version", target, "cmd_id", id)
	}
	return true
}
