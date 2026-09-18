// internal/backend/agent_deploy_core.go
package backend

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

// Общее ядро назначения обновления агента. Зовут мастер и дашборд
// (wizardDeployHandler) и мини-апп (miniapp_agent_update.go). Одно ядро
// намеренно: разойдись проверки -- админ в приложении смог бы то, что
// дашборд запрещает (даунгрейд, второе назначение поверх первого).

const (
	deployErrNoRelease = "no_release"
	deployErrDowngrade = "downgrade_rejected"
	deployErrPending   = "deploy_pending"
)

type agentDeployOpts struct {
	// RepoBaseURL -- публичный адрес бэкенда без хвоста ("https://host"); к
	// нему дописывается /v1/releases/download.
	RepoBaseURL string
	// ResolveIP -- IPv4 для curl --resolve на роутере. Ленивая: мастер
	// разрешает имя через DNS, и делать это ради отказа нельзя.
	ResolveIP      func() string
	AllowDowngrade bool
	Now            time.Time
	Source         string
}

type agentDeployResult struct {
	CmdID         string
	TargetVersion string
}

type agentDeployError struct {
	Status  int
	Code    string
	Message string
}

func (e *agentDeployError) Error() string { return e.Code + ": " + e.Message }

func agentDeployCore(d Deps, u *db.User, target string, opts agentDeployOpts) (agentDeployResult, *agentDeployError) {
	if d.DB == nil || d.CommandSink == nil {
		return agentDeployResult{}, &agentDeployError{http.StatusServiceUnavailable, errCodeInternal, "command sink not configured"}
	}
	targetVersion, err := releaseorigin.ValidateReleaseTag(target)
	if err != nil {
		return agentDeployResult{}, &agentDeployError{http.StatusConflict, deployErrNoRelease, err.Error()}
	}
	// Запрет даунгрейда -- тот же, что у переустановки (isVersionDowngrade):
	// старую, уже залатанную сборку не протащить мимо человека, забывшего про флаг.
	if !opts.AllowDowngrade && isVersionDowngrade(targetVersion, stringValue(u.LastDeployedVersion)) {
		return agentDeployResult{}, &agentDeployError{http.StatusBadRequest, deployErrDowngrade,
			fmt.Sprintf("target version %s is older than the currently installed %s — pass allow_downgrade to override",
				targetVersion, stringValue(u.LastDeployedVersion))}
	}
	if pending := strings.TrimSpace(stringValue(u.PendingVersion)); pending != "" {
		// Более свежая версия вытесняет назначенную. Иначе выключенный роутер,
		// которому однажды назначили v0.37, отбивал бы каждую следующую
		// раскатку и, проснувшись, ставил бы старьё (18.09: bronya и
		// caredns-oldcar ждали v0.37 при бэкенде v0.42). Та же или более старая
		// версия -- прежний отказ: повтор безопасен.
		if !isVersionDowngrade(pending, targetVersion) {
			return agentDeployResult{}, &agentDeployError{http.StatusConflict, deployErrPending, "agent already has pending deploy " + pending}
		}
		// Снимаем только ту отметку, которую видели: если её уже переписали,
		// проигрываем гонку тем же отказом, что и раньше.
		cleared, err := d.DB.Users().ClearPendingDeployIfMatches(u.ID, pending)
		if err != nil {
			return agentDeployResult{}, &agentDeployError{http.StatusInternalServerError, errCodeInternal, err.Error()}
		}
		if !cleared {
			return agentDeployResult{}, &agentDeployError{http.StatusConflict, deployErrPending, "agent already has pending deploy"}
		}
		d.CommandSink.DropPending(u.ID, "self_update")
		if d.Logger != nil {
			d.Logger.Info("agent deploy superseded", "source", opts.Source, "nickname", u.Nickname, "user_id", u.ID,
				"from_version", pending, "target_version", targetVersion)
		}
	}
	id, err := newCmdID()
	if err != nil {
		return agentDeployResult{}, &agentDeployError{http.StatusInternalServerError, errCodeInternal, "id gen: " + err.Error()}
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	resolveIP := ""
	if opts.ResolveIP != nil {
		resolveIP = opts.ResolveIP()
	}
	cmd := buildSelfUpdateCommand(id, targetVersion, opts.RepoBaseURL, resolveIP, now)
	if err := d.DB.Users().MarkPendingDeploy(u.ID, targetVersion, now.Format(time.RFC3339)); err != nil {
		if errors.Is(err, db.ErrDeployPending) {
			return agentDeployResult{}, &agentDeployError{http.StatusConflict, deployErrPending, "agent already has pending deploy"}
		}
		return agentDeployResult{}, &agentDeployError{http.StatusInternalServerError, errCodeInternal, err.Error()}
	}
	if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
		if _, clearErr := d.DB.Users().ClearPendingDeployIfMatches(u.ID, targetVersion); clearErr != nil && d.Logger != nil {
			d.Logger.Warn("rollback pending deploy after enqueue failure",
				"nickname", u.Nickname, "user_id", u.ID, "target_version", targetVersion, "err", clearErr)
		}
		return agentDeployResult{}, &agentDeployError{http.StatusInternalServerError, errCodeInternal, "enqueue: " + err.Error()}
	}
	if d.Logger != nil {
		d.Logger.Info("agent deploy enqueued",
			"source", opts.Source, "nickname", u.Nickname, "user_id", u.ID, "cmd_id", id, "target_version", targetVersion)
	}
	return agentDeployResult{CmdID: id, TargetVersion: targetVersion}, nil
}

// cancelAgentDeploy снимает назначенное обновление: выбрасывает команду из
// очереди (иначе спящий роутер, проснувшись, всё равно её получит) и снимает
// отметку безусловно. Повторный вызов безопасен: cleared=false.
func cancelAgentDeploy(d Deps, u *db.User) (cleared bool, dropped int, err error) {
	if d.CommandSink != nil {
		dropped = len(d.CommandSink.DropPending(u.ID, "self_update"))
	}
	cleared, err = d.DB.Users().ClearPendingDeploy(u.ID)
	return cleared, dropped, err
}
