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
// вышел на связь: перед выдачей команд в GET /v1/cmd и на отчёте.
//
// Зачем перед выдачей: включившийся агент первым делом опрашивает команды и
// только потом отчитывается (cmd/agent/main.go:132 против :141). Досылка
// только на отчёте опаздывала на весь первый опрос.
//
// Намерение оператора хранится в базе, поэтому источником правды остаётся
// она: пока стоит pending_version, каждый контакт без активной команды --
// новая попытка. Двойную выдачу исключает HasActiveCommand: активной
// считается и непротухшая в очереди, и выданная, пока не отжила свой TTL.
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
	if checker, ok := d.CommandSink.(activeCommandChecker); ok && checker.HasActiveCommand(uid, "self_update") {
		return
	}
	if st.Attempts >= pendingDeployMaxAttempts {
		reason := lostAttemptsText(st.Attempts)
		if strings.TrimSpace(st.LastError) != "" {
			reason = deployFailureText(st.LastError)
		}
		giveUpPendingDeploy(d, uid, nickname, st.Version, reason)
		return
	}
	enqueuePendingDeploy(d, uid, nickname, st.Version, base, now)
}

// enqueuePendingDeploy кладёт свежую команду обновления. Прежняя протухшая
// команда того же действия вытесняется очередью сама (queue.go supersede).
func enqueuePendingDeploy(d Deps, uid int64, nickname, target, base string, now time.Time) bool {
	id, err := newCmdID()
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on contact: id gen failed", "nickname", nickname, "err", err)
		}
		return false
	}
	cmd := buildSelfUpdateCommand(id, target, base, d.PublicIP, now)
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
