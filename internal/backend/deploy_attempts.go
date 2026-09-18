// internal/backend/deploy_attempts.go
package backend

import (
	"context"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// pendingDeployMaxAttempts -- сколько раз команда обновления уходит агенту,
// прежде чем мы признаём, что обновление на этом роутере не ставится.
// Без предела роутер, на котором своп заведомо падает, получал бы команду
// каждые полчаса вечно, а люди так и не узнали бы, что происходит.
const pendingDeployMaxAttempts = 3

// pendingDeployMaxAge -- сколько живёт намерение обновить роутер. Роутер,
// включённый через квартал, обновлять «по старой памяти» нельзя: за это время
// цель могла устареть, а человек -- забыть, что назначал.
const pendingDeployMaxAge = 90 * 24 * time.Hour

// pendingDeployExpired -- просрочено ли намерение. Нечитаемая дата просрочкой
// не считается: снимать то, чего не можем прочесть, нельзя.
func pendingDeployExpired(since string, now time.Time) bool {
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(since))
	if err != nil {
		return false
	}
	return now.Sub(ts) > pendingDeployMaxAge
}

// deployFailureText -- причина неудачи по-русски. Строка агента машинная и
// английская; людям и экрану «Парк» нужно последствие, без внутренних имён.
func deployFailureText(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "":
		return "агент не сообщил причину"
	case isReleaseProxyBusyFailure(raw):
		return "сервер обновлений был занят другими роутерами, повторим"
	case strings.Contains(s, "insufficient /opt space"), strings.Contains(s, "df /opt"):
		return "мало свободного места в разделе /opt"
	// Единственный источник этого текста -- releaseorigin.ValidateRepoBase*
	// (см. internal/agent/actions/self_update.go, validateSelfUpdateRepoBase):
	// голые "repo_base"/"backend url" были шире, чем сама ошибка, и ловили
	// ошибку скачивания, чей URL сам содержал одно из этих слов (B2).
	case strings.Contains(s, "not an allowed release origin"):
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

// legacyBusyOutput -- вывод старого агента (≤ v0.44), упёршегося в занятый
// прокси релизов: «download <файл>: HTTP 503 for <адрес зеркала>». Маркера
// wire.SelfUpdateBusyMarker он не знает, а 503 наш прокси отдаёт только на
// «занято» (release_proxy.go). Адрес обязан быть нашим зеркалом
// /v1/releases/download/: 503 от GitHub -- чужая беда, не очередь у нас.
var legacyBusyOutput = regexp.MustCompile(`download [^:\s]+: HTTP 503 for \S+/v1/releases/download/`)

// isReleaseProxyBusyFailure -- неудача self_update из-за занятого прокси
// релизов, а не из-за роутера.
func isReleaseProxyBusyFailure(output string) bool {
	return strings.Contains(output, wire.SelfUpdateBusyMarker) || legacyBusyOutput.MatchString(output)
}

// activeCommandReleaser -- узкая часть очереди: отпустить выданную команду
// раньше TTL. Тот же узкий интерфейс-по-типу, что activeCommandChecker.
type activeCommandReleaser interface {
	ReleaseActive(userID int64, cmdID string, cooldown time.Duration) bool
}

// deployBusyCooldown -- пауза перед повтором после «занято». Минута-две с
// разбросом: роутер опрашивает раз в минуту, и без разброса все отказники
// вернулись бы к прокси одной волной. Переменная -- ради тестов.
var deployBusyCooldown = func() time.Duration {
	return time.Minute + rand.N(time.Minute) // #nosec G404 -- разброс повторов, не секрет
}

// recordPendingDeployBusy -- ответ агента «прокси релизов занят». Попытку не
// тратит (прод 18.09: толпа у прокси исчерпала три попытки здоровому роутеру,
// и обновление ему сдалось), причину пишет и отпускает команду: следующий
// контакт после короткой паузы положит новую, не дожидаясь получасового TTL.
func recordPendingDeployBusy(d Deps, uid int64, nickname, cmdID, target, output string) {
	if d.DB == nil || strings.TrimSpace(target) == "" {
		return
	}
	attempts, matched, err := d.DB.Users().RecordPendingDeployBusy(uid, target, output)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("pending deploy busy record failed", "nickname", nickname, "target_version", target, "err", err)
		}
		return
	}
	if !matched {
		return
	}
	cooldown := deployBusyCooldown()
	if r, ok := d.CommandSink.(activeCommandReleaser); ok {
		r.ReleaseActive(uid, cmdID, cooldown)
	}
	if d.Logger != nil {
		d.Logger.Info("pending deploy hit busy release proxy; attempt not counted",
			"nickname", nickname, "target_version", target, "attempts", attempts,
			"retry_after", cooldown.String(), "output", output)
	}
}

// giveUpIfExhausted сдаётся, если попытки назначенного обновления уже
// исчерпаны, и сообщает, сдался ли. Решение принимается только здесь --
// на отчёте (handler.go), после того как версия агента доказана. Опрос
// (deploy_wake.go) версии не знает и права объявлять «не ставится» не имеет:
// иначе роутер, включившийся раньше своего первого отчёта, получал бы
// ложную тревогу, даже если на самом деле уже стоит на цели (review
// Important #1).
func giveUpIfExhausted(d Deps, uid int64, nickname string) bool {
	if d.DB == nil {
		return false
	}
	// Выданная команда ещё в работе: агент качает или меняет бинарь, а
	// отчёт идёт параллельно со старой версией. Сдаваться рано -- своп может
	// пройти, и тогда люди получили бы ложное «не ставится» без последующего
	// «обновлено» (final review I1). Потерянная попытка перестаёт быть
	// активной по TTL, и сдача случится на первом отчёте после него.
	if checker, ok := d.CommandSink.(activeCommandChecker); ok && checker.HasActiveCommand(uid, "self_update") {
		return false
	}
	st, err := d.DB.Users().PendingDeploy(uid)
	if err != nil {
		if d.Logger != nil {
			d.Logger.Warn("deploy give-up check: read pending failed", "nickname", nickname, "err", err)
		}
		return false
	}
	if st.Version == "" || st.Attempts < pendingDeployMaxAttempts {
		return false
	}
	reason := lostAttemptsText(st.Attempts)
	if strings.TrimSpace(st.LastError) != "" {
		reason = deployFailureText(st.LastError)
	}
	giveUpPendingDeploy(d, uid, nickname, st.Version, reason)
	return true
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
