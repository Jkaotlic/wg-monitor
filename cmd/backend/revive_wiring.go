package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// newReviveService собирает оживление агента или возвращает nil, если ключа
// нет или он негоден: бэкенд при этом стартует, а функция выключена.
//
// Recover зовётся здесь же, СИНХРОННО, до возврата -- carry #5 (мандатное
// ревью): вызывающий (main) кладёт результат прямо в Deps.Revive и только
// потом запускает Run отдельной горутиной и открывает HTTP-порт, так что ни
// один Schedule/Cancel через мини-апп не может прийти раньше, чем прерванные
// рестартом переустановки вернутся из running в waiting.
func newReviveService(ctx context.Context, cfg *backend.Config, d *db.DB, provisionDeps provision.Deps, sender notify.Sender, logger *slog.Logger) *revive.Service {
	log := logger.With("component", "revive")
	key, err := revive.LoadKey(cfg.Revive.KeyFile)
	if errors.Is(err, revive.ErrKeyNotConfigured) {
		log.Info("оживление агента выключено: revive.key_file не задан")
		return nil
	}
	if err != nil {
		log.Warn("оживление агента выключено", "reason", err)
		return nil
	}
	defer clear(key)

	svc, err := revive.New(revive.Config{
		DB:  d,
		Key: key,
		Engine: backend.NewReviveEngine(backend.ReviveEngineDeps{
			DB: d, Provision: provisionDeps, PublicBaseURL: cfg.PublicBaseURL, PublicIP: cfg.PublicIP, Logger: log,
		}),
		Notifier: notify.NewFanout(d, sender, log, cfg.Telegram.AdminUserID),
		// BaseCtx -- carry #5 (мандатное ревью): ctx процесса (отменяется на
		// SIGINT/SIGTERM), НЕ context.Background(). confirmSoon (run.go)
		// живёт на этом ctx, и остановка бэкенда обязана оборвать его же, а
		// не бессмертную фоновую проверку.
		BaseCtx: ctx,
		Logger:  log,
		// Слишком старый агент на связи -- не «ожил сам»: его обновит только
		// переустановка (v0.45, авто-оживление).
		NeedsReinstall: backend.AgentNeedsReinstall,
	})
	if err != nil {
		log.Warn("оживление агента выключено", "reason", err)
		return nil
	}
	if n, err := svc.Recover(); err != nil {
		log.Warn("оживление: возврат прерванных переустановок не удался", "err", err)
	} else if n > 0 {
		log.Info("оживление: прерванные переустановки возвращены в ожидание", "count", n)
	}
	log.Info("оживление агента включено")
	return svc
}

// waitBounded ждёт wait не дольше d. Остановка бэкенда (финальное ревью
// 15.09, M2): фоновые проверки оживления, запущенные постановкой, не должны
// писать в уже закрытую базу, но и зависшая проба панели не должна держать
// процесс после SIGTERM. false -- не дождались.
func waitBounded(wait func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
