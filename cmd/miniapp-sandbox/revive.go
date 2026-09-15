package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// sandboxRevive -- оживление агента в песочнице: в памяти, без ключа
// шифрования, без воркера и без панели роутера. Настоящий сервис (часть 1)
// требует ключ в secrets/ и ходит в панель -- в песочнице нет ни того, ни
// другого, а экран «Парк» проверить нужно.
//
// Пароль песочница не хранит вовсе и в консоль не пишет: даже фальшивый
// секрет в журнале приучил бы к тому, что так можно.
type sandboxRevive struct {
	mu      sync.Mutex
	enabled bool
	noPanel map[int64]bool
	views   map[int64]*revive.IntentView
}

var _ backend.ReviveAPI = (*sandboxRevive)(nil)

func newSandboxRevive(enabled bool, noPanel map[int64]bool) *sandboxRevive {
	return &sandboxRevive{enabled: enabled, noPanel: noPanel, views: map[int64]*revive.IntentView{}}
}

func (s *sandboxRevive) Enabled() bool { return s.enabled }

// seedState кладёт намерение в нужном состоянии -- чтобы снять каждую строку
// экрана, не дожидаясь воркера, которого здесь нет.
func (s *sandboxRevive) seedState(routerID int64, state string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := &revive.IntentView{Status: state, ExpiresAt: now.AddDate(0, 0, 27)}
	switch state {
	case "waiting":
		v.LastProbeAt = now.Add(-2 * time.Minute)
		v.LastProbeText = "не отвечает"
	case "failed":
		v.Attempts = 1
		v.LastErrorText = "пароль не подошёл"
	case "expired":
		v.ExpiresAt = now.Add(-time.Hour)
	}
	s.views[routerID] = v
}

func (s *sandboxRevive) Schedule(_ context.Context, routerID int64, req revive.ScheduleRequest) (revive.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return revive.Intent{}, &revive.Error{Code: "revive_disabled"}
	}
	// Как в настоящем сервисе: пароль root обязателен (пре-флайт 15.09),
	// пароль из одних пробелов -- всё равно что пустой.
	if strings.TrimSpace(req.RootPassword) == "" {
		return revive.Intent{}, &revive.Error{Code: "no_credentials"}
	}
	if cur, ok := s.views[routerID]; ok && cur.Status == "running" {
		return revive.Intent{}, &revive.Error{Code: "revive_running"}
	}
	if s.noPanel[routerID] && req.AWGMURL == "" {
		return revive.Intent{}, &revive.Error{Code: "no_awgm_url"}
	}
	if s.noPanel[routerID] && req.AWGMURL != "" {
		// Адрес записан -- дальше роутер как все: повтор с адресом отказывает.
		s.noPanel[routerID] = false
	} else if req.AWGMURL != "" {
		return revive.Intent{}, &revive.Error{Code: "awgm_url_already_set"}
	}
	days := req.ExpiresDays
	if days == 0 {
		days = 30
	}
	now := time.Now().UTC()
	v := &revive.IntentView{Status: "waiting", ExpiresAt: now.AddDate(0, 0, days)}
	s.views[routerID] = v
	slog.Info("песочница: оживление поставлено", "router_id", routerID, "expires_days", days)
	return revive.Intent{Status: v.Status, ExpiresAt: v.ExpiresAt}, nil
}

func (s *sandboxRevive) Cancel(_ context.Context, routerID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[routerID]
	if ok && v.Status == "running" {
		// Как в настоящем сервисе: идущую переустановку не отменить.
		return false, &revive.Error{Code: "revive_running"}
	}
	if !ok || v.Status != "waiting" {
		return false, nil
	}
	delete(s.views, routerID)
	slog.Info("песочница: оживление отменено", "router_id", routerID)
	return true, nil
}

func (s *sandboxRevive) StatusFor(routerID int64) (*revive.IntentView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[routerID]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}
