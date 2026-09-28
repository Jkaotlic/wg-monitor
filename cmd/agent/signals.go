package main

import (
	"context"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/exitprobe"
	"github.com/Jkaotlic/wg-monitor/internal/agent/facts"
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
	"github.com/Jkaotlic/wg-monitor/internal/agent/pingruns"
	"github.com/Jkaotlic/wg-monitor/internal/agent/wakehook"
	"github.com/Jkaotlic/wg-monitor/internal/agent/wanfacts"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// agentSignals -- всё, что v0.47 добавляет к агенту: замерщик адреса выхода,
// свёртка журнала пингчека, линии провайдера, ndm-хук и сборщик фактов.
type agentSignals struct {
	prober    *exitprobe.Prober
	tracker   *pingruns.Tracker
	wan       *wanfacts.Collector
	watcher   *wakehook.Watcher
	hookState string
	hub       *facts.Hub
}

func buildSignals(cfg *agent.Config, awgClient *awgmgr.Client, hookDir, wakeFile string) agentSignals {
	s := agentSignals{
		prober:  &exitprobe.Prober{Client: awgClient},
		tracker: &pingruns.Tracker{Src: awgClient},
		wan:     &wanfacts.Collector{Client: awgClient},
	}
	// Ping-Check профиль на линии провайдера (Task 15a, сверка С3): running-
	// config несёт профиль привязанным к приоритету линии (`ip global N`
	// внутри `interface <NDMS-имя>`), а /api/wan/status -- тот же приоритет
	// под kernel-именем интерфейса (eth3, cdc_br0...). Тот же доступ к ndmc,
	// что и у readDNSEndpoints (cmd/agent/main.go) -- второй путь к прошивке
	// не заводим.
	ndmc := keenetic.NDMC{Runner: checks.OSExec{}}
	s.wan.PingCheck = func(ctx context.Context) (map[string]string, error) {
		rc, err := ndmc.Show(ctx, "running-config")
		if err != nil {
			return nil, err
		}
		st, err := awgClient.WANStatus(ctx)
		if err != nil {
			return nil, err
		}
		priorities := make(map[string]int, len(st.Interfaces))
		for name, it := range st.Interfaces {
			priorities[name] = it.Priority
		}
		return keenetic.WANPingCheckByPriority(rc, priorities), nil
	}
	state, errText := wakehook.Ensure(hookDir, wakeFile, !cfg.Agent.WakeHooksOff)
	s.hookState = state
	s.watcher = &wakehook.Watcher{Path: wakeFile, Throttle: &wakehook.Throttle{}, Since: time.Now().UTC()}
	watcher := s.watcher
	s.hub = &facts.Hub{
		Exit:  s.prober.Snapshot,
		Ping:  s.tracker,
		WAN:   s.wan.Fact,
		Hooks: func() *wire.HookFacts { return watcher.Facts(state, errText) },
	}
	return s
}

// start запускает фоновые петли; wake -- как разбудить отчёт.
func (s agentSignals) start(ctx context.Context, wake func()) {
	s.watcher.Wake = wake
	go s.prober.Run(ctx)
	if s.hookState == wakehook.StateInstalled {
		go s.watcher.Run(ctx)
	}
}
