package backend

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/releaseorigin"
)

// ReviveEngineDeps -- то, что нужно движку переустановки вне HTTP-запроса.
// Store общий с дашбордом: замок по имени роутера один на всех.
type ReviveEngineDeps struct {
	DB            *db.DB
	Provision     provision.Deps
	PublicBaseURL string
	PublicIP      string
	AWGMRelayPath string
	Logger        *slog.Logger
}

type reviveEngine struct{ d Deps }

// NewReviveEngine отдаёт оживлению тот же движок, что у кнопки
// «Переустановить» в дашборде (startRepairReinstall).
func NewReviveEngine(ed ReviveEngineDeps) revive.Engine {
	return &reviveEngine{d: Deps{
		DB: ed.DB, Provision: ed.Provision, PublicBaseURL: ed.PublicBaseURL,
		PublicIP: ed.PublicIP, AWGMRelayPath: ed.AWGMRelayPath, Logger: ed.Logger,
	}}
}

// reviveLaunchPreflightTimeout -- бюджет на подготовку задания (версия +
// чексуммы -- сетевые походы к GitHub), НЕ на саму установку: сама установка
// форкается в горутину внутри provision.Store.Start и живёт на
// ReviveEngineDeps.Provision.BaseCtx (долгоживущий ctx процесса), а не на ctx
// этого вызова. Carry #4 (мандатное ревью): ctx, которым зовут Launch, --
// это ctx воркера revive (в конце концов Run(ctx), отменяемый при остановке
// бэкенда). Если использовать его напрямую, гонка "процесс завершается ровно
// в момент запуска" оборвала бы поход за версией/чексуммами на середине --
// задание не создалось бы вовсе, хотя секрет уже расшифрован и попытка уже
// списана. Отвязанный таймаут переживает отмену вызывающего ctx: подготовка
// либо успевает (обычный случай, доли секунды до пары секунд на сеть), либо
// отказывает по СВОЕЙ причине, а не потому что бэкенд просто перезапускается.
const reviveLaunchPreflightTimeout = 30 * time.Second

func (e *reviveEngine) Launch(ctx context.Context, routerID int64, s revive.Secrets, targetVersion string) (string, error) {
	if e.d.DB == nil {
		return "", &revive.LaunchError{Permanent: true, Text: "сервер не настроен на переустановку"}
	}
	// Перечитываем строку роутера прямо перед запуском: awgm_url или
	// архитектура могли смениться между опросом (checkOne) и этим вызовом --
	// намерение живёт часами, а Launch должно бить по свежим данным, а не по
	// снимку, сделанному воркером раньше.
	u, err := e.d.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) {
		return "", &revive.LaunchError{Permanent: true, Text: "роутер удалён из парка"}
	}
	if err != nil {
		return "", &revive.LaunchError{Text: "переустановка не запустилась"}
	}
	version := strings.TrimSpace(targetVersion)
	if version == "" {
		version = reviveBackendVersion()
	}

	launchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reviveLaunchPreflightTimeout)
	defer cancel()

	// AllowDowngrade остаётся false: оживление не имеет права тихо откатить
	// агента ниже уже установленной версии просто потому, что сама сборка
	// бэкенда старее. Если версия для запуска ниже LastDeployedVersion,
	// startRepairReinstall вернёт downgrade_rejected -> reviveLaunchError
	// делает его Permanent -- окончательный отказ, который экран покажет
	// оператору, а не тихий откат агента без его ведома.
	jobID, _, serr := startRepairReinstall(launchCtx, e.d, u.Nickname, u, reinstallInput{
		RootPassword: s.RootPassword(),
		AWGMLogin:    s.AWGMLogin(),
		AWGMPassword: s.AWGMPassword(),
		AWGMAPIKey:   s.AWGMAPIKey(),
		Version:      version,
	})
	if serr != nil {
		return "", reviveLaunchError(serr)
	}
	return jobID, nil
}

func (e *reviveEngine) Outcome(jobID string) (revive.Outcome, bool) {
	if e.d.Provision.Store == nil {
		return revive.Outcome{}, false
	}
	job, ok := e.d.Provision.Store.Get(jobID)
	if !ok {
		return revive.Outcome{}, false
	}
	switch job.State {
	case provision.StateRunning:
		return revive.Outcome{}, true
	case provision.StateSuccess:
		return revive.Outcome{Finished: true, Success: true, Version: job.Version}, true
	}
	// Ошибка входа узнаётся СТРОГО по job.Hint == provision.HintAuthFailed --
	// структурному полю, которое runner.go проставляет сам после разбора
	// причины отказа терминала. Никакого поиска "401"/"403" по тексту здесь
	// нет и не должно быть (carry #2, мандатное ревью): посторонняя ошибка,
	// в которой случайно встретились эти цифры, не должна выглядеть как
	// "пароль не подошёл" и стирать ещё годный секрет раньше времени.
	if job.Hint == provision.HintAuthFailed {
		return revive.Outcome{Finished: true, AuthFailed: true, Text: "пароль не подошёл"}, true
	}
	return revive.Outcome{Finished: true, Text: reviveStepText(reviveFailedStep(job.Steps))}, true
}

// reviveBackendVersion -- «пусто = версия бэкенда на момент запуска». Сборка
// без тега выпуска (dev, unknown) даёт пусто, и движок берёт последнюю
// опубликованную версию, как у дашборда.
func reviveBackendVersion() string {
	if v, err := releaseorigin.ValidateReleaseTag(serverVersion); err == nil {
		return v
	}
	return ""
}

// reviveLaunchError -- код отказа движка в русскую причину. Message движка
// (английский, иногда с адресами) наружу не идёт.
func reviveLaunchError(serr *repairStartError) *revive.LaunchError {
	switch serr.Code {
	case "no_awgm_url":
		return &revive.LaunchError{Permanent: true, Text: "у роутера нет адреса панели"}
	case "downgrade_rejected":
		return &revive.LaunchError{Permanent: true, Text: "версия сервера старее установленного агента"}
	case "no_public_base_url", "provision_not_configured", "db_not_configured":
		return &revive.LaunchError{Permanent: true, Text: "сервер не настроен на переустановку"}
	case "invalid_nickname", "invalid_kind":
		return &revive.LaunchError{Permanent: true, Text: "имя роутера не подходит для переустановки"}
	case "already_running", "provision_already_running":
		// NoAttempt (Fix round 1, Minor #3, мандатное ревью): движок занят
		// ЧУЖИМ заданием на этом же роутере (дашборд уже чинит или ставит)
		// -- не вина оживления, воркер не тратит на это одну из пяти попыток.
		return &revive.LaunchError{NoAttempt: true, Text: "на роутере уже идёт другая установка или ремонт"}
	case "latest_version_failed":
		return &revive.LaunchError{Text: "не удалось узнать последнюю версию агента"}
	case "checksums_failed":
		return &revive.LaunchError{Text: "не удалось проверить подпись выпуска"}
	default:
		return &revive.LaunchError{Text: "переустановка не запустилась"}
	}
}

func reviveFailedStep(steps []provision.Step) string {
	for _, st := range steps {
		if st.Status == provision.StepFailed {
			return st.Name
		}
	}
	return ""
}

func reviveStepText(step string) string {
	switch step {
	case provision.StepTerminalConnected:
		return "не удалось подключиться к панели роутера"
	case provision.StepArchDetected:
		return "роутер сообщил неподдерживаемую архитектуру"
	case provision.StepDownloading:
		return "роутер не смог скачать агент"
	case provision.StepChecksumOK:
		return "скачанный агент не прошёл проверку подписи"
	case provision.StepConfigWritten, provision.StepInitInstalled, provision.StepServiceStarted:
		return "агент поставлен, но не запустился"
	case provision.StepVerifyOnline:
		return "агент поставлен, но не вышел на связь"
	default:
		return "переустановка не удалась"
	}
}
