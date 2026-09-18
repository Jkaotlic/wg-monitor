package backend

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// Авто-оживление давно не обновлявшихся роутеров (v0.45, задача B, п. 3).
//
// Решение оператора 18.09: «пароль root хранится зашифрованным и используется
// для авто-оживления». Фоновый проход рядом с движком оживления: кто «давно не
// обновлялся» (agentLongNotUpdated) -- тем revive.Service.AutoSchedule ставит
// оживление по сохранённому паролю. Можно ли и не рано ли -- решает сервис
// (нет пароля, нет годного адреса, уже идёт, сутки после итога, отмена
// админа); здесь -- кого.

const (
	// autoReviveEvery -- как часто проход смотрит парк. Решения держатся на
	// строках базы, а не на памяти процесса: частота влияет только на то,
	// как быстро замечается новый «давно не обновлявшийся».
	autoReviveEvery = 30 * time.Minute
	// autoReviveFirstAfter -- первый проход не сразу после старта: роутеры
	// на связи успевают отчитаться, и свежесть агента видна по-настоящему.
	autoReviveFirstAfter = 5 * time.Minute
)

// AutoReviver -- то, что проходу нужно от сервиса оживления.
type AutoReviver interface {
	Enabled() bool
	AutoSchedule(ctx context.Context, routerID int64) (revive.AutoOutcome, error)
}

var _ AutoReviver = (*revive.Service)(nil)

type AutoReviveDeps struct {
	DB          *db.DB
	Revive      AutoReviver
	CommandSink CommandSink
	Logger      *slog.Logger
	Now         func() time.Time
}

// AgentNeedsReinstall -- агент ниже agentSelfUpdateFloor: даже на связи его
// обновит только переустановка. Отдаётся воркеру оживления как
// revive.Config.NeedsReinstall, чтобы такой агент не считался «ожил сам».
func AgentNeedsReinstall(u *db.User) bool {
	if u == nil {
		return false
	}
	return agentUpdateVerdictFor(stringValue(u.LastDeployedVersion), serverVersion).TooOld
}

// RunAutoRevive -- проход через autoReviveFirstAfter после старта и затем раз
// в autoReviveEvery, пока ctx жив. Без сервиса оживления (нет ключа) --
// сразу выходит.
func RunAutoRevive(ctx context.Context, ad AutoReviveDeps) {
	if ad.DB == nil || ad.Revive == nil || !ad.Revive.Enabled() {
		return
	}
	t := time.NewTimer(autoReviveFirstAfter)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			autoRevivePass(ctx, ad)
			t.Reset(autoReviveEvery)
		}
	}
}

// autoRevivePass -- один проход; итог по исходам -- для журнала и тестов.
func autoRevivePass(ctx context.Context, ad AutoReviveDeps) map[revive.AutoOutcome]int {
	sum := map[revive.AutoOutcome]int{}
	if ad.DB == nil || ad.Revive == nil || !ad.Revive.Enabled() {
		return sum
	}
	logger := ad.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := time.Now().UTC()
	if ad.Now != nil {
		now = ad.Now().UTC()
	}
	users, err := ad.DB.Users().GetAll()
	if err != nil {
		logger.Warn("авто-оживление: роутеры не прочитаны", "err", err)
		return sum
	}
	for i := range users {
		if ctx.Err() != nil {
			return sum
		}
		u := &users[i]
		if !agentLongNotUpdated(u, serverVersion, now) {
			continue
		}
		verdict := agentUpdateVerdictFor(stringValue(u.LastDeployedVersion), serverVersion)
		reason := "silent_behind"
		if verdict.TooOld {
			reason = "too_old"
		}
		out, err := ad.Revive.AutoSchedule(ctx, u.ID)
		if err != nil {
			// Только код: текст ошибки сервиса несёт что угодно.
			logger.Warn("авто-оживление: постановка не удалась", "router_id", u.ID, "nickname", u.Nickname, "code", reviveErrorCode(err))
			continue
		}
		sum[out]++
		switch out {
		case revive.AutoScheduled:
			logger.Info("авто-оживление: поставлено", "router_id", u.ID, "nickname", u.Nickname,
				"reason", reason, "agent_version", stringValue(u.LastDeployedVersion))
		default:
			logger.Debug("авто-оживление: пропущено", "router_id", u.ID, "nickname", u.Nickname, "reason", reason, "outcome", string(out))
		}
		// Назначенное self_update слишком старому агенту бесполезно: он его не
		// выполнит (нет self_update вовсе), а оживление поставит свежую версию
		// само. Отстающему, но умеющему -- оставляем: проснётся живым --
		// обновится сам, и оживление закроется «ожил сам».
		if verdict.TooOld && (out == revive.AutoScheduled || out == revive.AutoActive) &&
			stringValue(u.PendingVersion) != "" {
			cleared, dropped, err := cancelAgentDeploy(Deps{DB: ad.DB, CommandSink: ad.CommandSink}, u)
			if err != nil {
				logger.Warn("авто-оживление: назначенное обновление не снято", "router_id", u.ID, "err", err)
			} else if cleared || dropped > 0 {
				logger.Info("авто-оживление: снято бесполезное обновление слишком старого агента",
					"router_id", u.ID, "nickname", u.Nickname, "pending_version", stringValue(u.PendingVersion), "dropped", dropped)
			}
		}
	}
	if sum[revive.AutoScheduled] > 0 {
		logger.Info("авто-оживление: проход", "scheduled", sum[revive.AutoScheduled],
			"no_root_password", sum[revive.AutoNoPassword], "no_panel_address", sum[revive.AutoNoPanelAddress])
	}
	return sum
}
