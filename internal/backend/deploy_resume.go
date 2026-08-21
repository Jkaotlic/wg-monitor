// internal/backend/deploy_resume.go
package backend

import (
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// deployEnqueuer -- узкая часть CommandSink, нужная возобновлению. Отдельный
// интерфейс, чтобы тест не изображал всю очередь ради одного метода.
type deployEnqueuer interface {
	Enqueue(userID int64, cmd wire.Command) error
}

// ResumePendingDeploys заново ставит в очередь обновления, назначенные до
// рестарта.
//
// Очередь команд живёт в памяти, а отметка «обновление назначено» -- в базе
// (users.pending_version). Рестарт бэкенда стирал первое и оставлял второе:
// роутер месяцами числился «ждёт v0.18.5», хотя команды ему уже никто не
// пошлёт. В дашборде это выглядело ожиданием, а на деле было потерянной
// операцией -- ровно тот случай, когда состояние врёт о происходящем.
//
// Обработчик просроченных команд (deploy_expiry.go) эту дыру не закрывает: он
// срабатывает, когда команда протухает В ЖИВОЙ очереди, а при рестарте она
// исчезает молча.
//
// Намерение оператора хранится в базе, поэтому восстанавливается именно оно:
// та же версия, та же ссылка на бинарь. Команда, ушедшая роутеру, который
// сейчас офлайн, дождётся его в очереди -- как и в первый раз.
func ResumePendingDeploys(d *db.DB, sink deployEnqueuer, publicBaseURL, publicIP string, logger *slog.Logger) int {
	if d == nil || sink == nil {
		return 0
	}
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" {
		// Без публичного адреса ссылку на бинарь собрать нечем, и агент по
		// такой команде ничего не скачает. Промолчать здесь -- значит снова
		// оставить роутер в ожидании, которое ничем не кончится.
		if logger != nil {
			logger.Warn("resume deploys: public_base_url not set; pending deploys stay unqueued")
		}
		return 0
	}
	users, err := d.Users().GetAll()
	if err != nil {
		if logger != nil {
			logger.Warn("resume deploys: list users failed", "err", err)
		}
		return 0
	}
	resumed := 0
	for _, u := range users {
		target := strings.TrimSpace(stringValue(u.PendingVersion))
		if target == "" {
			continue
		}
		id, err := newCmdID()
		if err != nil {
			if logger != nil {
				logger.Warn("resume deploys: id gen failed", "nickname", u.Nickname, "err", err)
			}
			continue
		}
		cmd := wire.Command{
			ID:     id,
			Action: "self_update",
			Args: map[string]any{
				"version":   target,
				"repo_base": base + "/v1/releases/download",
			},
			IssuedAt: time.Now().UTC(),
		}
		if ip := strings.TrimSpace(publicIP); ip != "" {
			cmd.Args["repo_resolve_ip"] = ip
		}
		if err := sink.Enqueue(u.ID, cmd); err != nil {
			if logger != nil {
				logger.Warn("resume deploys: enqueue failed",
					"nickname", u.Nickname, "target_version", target, "err", err)
			}
			continue
		}
		resumed++
		if logger != nil {
			logger.Info("resume deploys: re-queued self_update after restart",
				"nickname", u.Nickname, "user_id", u.ID, "target_version", target,
				"pending_since", stringValue(u.PendingSince), "cmd_id", id)
		}
	}
	return resumed
}
