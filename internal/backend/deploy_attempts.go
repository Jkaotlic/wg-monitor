// internal/backend/deploy_attempts.go
package backend

import (
	"context"
	"strconv"
	"strings"
)

// pendingDeployMaxAttempts -- сколько раз команда обновления уходит агенту,
// прежде чем мы признаём, что обновление на этом роутере не ставится.
// Без предела роутер, на котором своп заведомо падает, получал бы команду
// каждые полчаса вечно, а люди так и не узнали бы, что происходит.
const pendingDeployMaxAttempts = 3

// deployFailureText -- причина неудачи по-русски. Строка агента машинная и
// английская; людям и экрану «Парк» нужно последствие, без внутренних имён.
func deployFailureText(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "":
		return "агент не сообщил причину"
	case strings.Contains(s, "insufficient /opt space"), strings.Contains(s, "df /opt"):
		return "мало свободного места в разделе /opt"
	case strings.Contains(s, "not an allowed release origin"), strings.Contains(s, "repo_base"), strings.Contains(s, "backend url"):
		return "адрес загрузки не совпал с адресом сервера"
	case strings.Contains(s, "sha256 mismatch"), strings.Contains(s, "signature"):
		return "файл обновления не прошёл проверку подлинности"
	case strings.Contains(s, "no entry for"):
		return "в выпуске нет файла для этого роутера"
	case strings.Contains(s, "older than the running"):
		return "на роутере уже стоит версия новее"
	case strings.Contains(s, "unsupported goarch"):
		return "архитектура роутера не поддерживается"
	case strings.Contains(s, "not a valid release tag"):
		return "такой версии нет среди выпусков"
	case strings.Contains(s, "download"), strings.Contains(s, "http "), strings.Contains(s, "dial tcp"),
		strings.Contains(s, "no such host"), strings.Contains(s, "timeout"):
		return "роутер не смог скачать обновление"
	default:
		return "агент сообщил об ошибке установки"
	}
}

// lostAttemptsText -- причина, когда агент брал команду, но ни разу не
// ответил, а версия так и не сменилась.
func lostAttemptsText(attempts int) string {
	return "команда уходила на роутер " + strconv.Itoa(attempts) + " раза, но версия не сменилась"
}

// countPendingDeployAttempt засчитывает выдачу команды обновления агенту.
func countPendingDeployAttempt(d Deps, uid int64, nickname, target string) {
	if d.DB == nil || strings.TrimSpace(target) == "" {
		return
	}
	n, matched, err := d.DB.Users().IncrementPendingAttempts(uid, target)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("pending deploy attempt count failed", "nickname", nickname, "target_version", target, "err", err)
		}
		return
	}
	if matched && d.Logger != nil {
		d.Logger.Info("pending deploy attempt dispatched", "nickname", nickname, "target_version", target, "attempt", n)
	}
}

// recordPendingDeployFailure обрабатывает ответ агента «не получилось»:
// запоминает причину, отметку оставляет, на пределе попыток -- сдаётся.
func recordPendingDeployFailure(d Deps, uid int64, nickname, target, output string) {
	if d.DB == nil || strings.TrimSpace(target) == "" {
		return
	}
	attempts, matched, err := d.DB.Users().RecordPendingDeployError(uid, target, output)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("pending deploy failure record failed", "nickname", nickname, "target_version", target, "err", err)
		}
		return
	}
	if !matched {
		if d.Logger != nil {
			d.Logger.Info("self_update failed without matching pending deploy", "nickname", nickname, "target_version", target)
		}
		return
	}
	if attempts < pendingDeployMaxAttempts {
		if d.Logger != nil {
			d.Logger.Info("pending deploy attempt failed; will retry on contact",
				"nickname", nickname, "target_version", target, "attempt", attempts, "output", output)
		}
		return
	}
	giveUpPendingDeploy(d, uid, nickname, target, deployFailureText(output))
}

// giveUpPendingDeploy снимает отметку, когда обновление заведомо не ставится,
// и пишет людям роутера. Счёт попыток и сырую причину оставляет: экран «Парк»
// показывает «не ставится: …», пока админ не назначит заново или не отменит.
func giveUpPendingDeploy(d Deps, uid int64, nickname, target, reason string) {
	cleared, err := d.DB.Users().ClearPendingDeployIfMatches(uid, target)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("pending deploy give-up clear failed", "nickname", nickname, "target_version", target, "err", err)
		}
		return
	}
	if !cleared {
		return
	}
	if d.Logger != nil {
		d.Logger.Warn("pending deploy given up", "nickname", nickname, "user_id", uid, "target_version", target, "reason", reason)
	}
	if d.DeployNotifier == nil {
		return
	}
	spawnRelay(d, "deploy-give-up", func(ctx context.Context) {
		if err := d.DeployNotifier.SendDeferredUpdate(ctx, uid, nickname, target, "err", reason); err != nil {
			incTGError()
			if d.Logger != nil {
				d.Logger.Warn("deploy give-up notify failed", "nickname", nickname, "err", err)
			}
		}
	})
}
