package revive

import (
	"context"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/awgmstate"
)

// Recover -- на старте бэкенда. Задания переустановки живут в памяти и после
// рестарта потеряны: все running возвращаются в waiting, попытка остаётся
// засчитанной (она была засчитана при запуске).
func (s *Service) Recover() (int64, error) {
	if !s.Enabled() {
		return 0, nil
	}
	n, err := s.cfg.DB.Revive().ResetRunning(reasonJobLost, s.now())
	if err != nil {
		return 0, err
	}
	if n > 0 {
		s.logger.Info("оживление: прерванные рестартом переустановки возвращены в ожидание", "count", n)
	}
	return n, nil
}

// Run -- цикл воркера: обход сразу и затем раз в ProbeEvery до отмены ctx.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	s.Tick(ctx)
	t := time.NewTicker(s.cfg.ProbeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Tick(ctx)
		}
	}
}

// confirmSoon -- проверка сразу после постановки: опрос сейчас и, пока панель
// отвечает, ещё через ConfirmGap -- до ReachableProbes опросов подряд. Если
// агент молчит, второй ответ запускает переустановку, не дожидаясь обхода.
func (s *Service) confirmSoon(routerID int64) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx := s.cfg.BaseCtx
		for i := 0; i < s.cfg.ReachableProbes; i++ {
			if i > 0 && !s.cfg.Sleep(ctx, s.cfg.ConfirmGap) {
				return
			}
			if ctx.Err() != nil {
				return
			}
			s.checkOne(ctx, routerID)
			in, err := s.cfg.DB.Revive().Get(routerID)
			if err != nil || in == nil || in.Status != StatusWaiting || in.LastProbeState != awgmstate.Reachable {
				return
			}
		}
	}()
}
