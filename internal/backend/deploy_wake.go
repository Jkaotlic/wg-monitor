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

// requeueDeployOnWake кладёт назначенное обновление обратно в очередь, когда
// роутер вышел на связь.
//
// Зачем: команда self_update живёт в очереди тридцать минут и выбрасывается в
// тот момент, когда агент за ней придёт. Мобильный роутер спит часами -- он
// приходил ровно за протухшей командой и получал пустоту, а отметка «ждёт
// обновления» снималась сама. Обновить такой роутер кнопкой было невозможно
// в принципе: один из мобильных просидел на версии четырёхмесячной давности,
// хотя деплой ему назначали не раз.
//
// Намерение оператора хранится в базе, поэтому источником правды остаётся
// она: пока стоит pending_version, каждое пробуждение -- новая попытка.
// Отметку здесь не трогаем; её снимут те, кто и снимал раньше -- совпавшая
// версия в отчёте или провал команды.
func requeueDeployOnWake(d Deps, uid int64, nickname, target string) {
	if d.CommandSink == nil || strings.TrimSpace(target) == "" {
		return
	}
	base := strings.TrimRight(strings.TrimSpace(d.PublicBaseURL), "/")
	if base == "" {
		// Без публичного адреса ссылку на бинарь собрать нечем -- агент по
		// такой команде ничего не скачает.
		return
	}
	if checker, ok := d.CommandSink.(activeCommandChecker); ok && checker.HasActiveCommand(uid, "self_update") {
		return
	}
	id, err := newCmdID()
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on wake: id gen failed", "nickname", nickname, "err", err)
		}
		return
	}
	cmd := buildSelfUpdateCommand(id, target, base, d.PublicIP, time.Now().UTC())
	if err := d.CommandSink.Enqueue(uid, cmd); err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy on wake: enqueue failed",
				"nickname", nickname, "target_version", target, "err", err)
		}
		return
	}
	if d.Logger != nil {
		d.Logger.Info("deploy on wake: re-queued self_update",
			"nickname", nickname, "user_id", uid, "target_version", target, "cmd_id", id)
	}
}
