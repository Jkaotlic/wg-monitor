package exitprobe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const (
	defaultEvery     = 5 * time.Minute
	defaultPerTunnel = 20 * time.Minute
	// awgmPause -- сколько не спрашивать awg-manager после двух отказов
	// подряд или ответа «не умею»: не долбим сломанное каждые 5 минут.
	awgmPause = time.Hour
	// firstDelay -- первый замер не в момент старта: агент и так занят
	// первым отчётом, а после перезагрузки роутера туннели ещё поднимаются.
	firstDelay = time.Minute
)

// Prober меряет адрес выхода по кругу, по одному VPN-туннелю раз в Every,
// каждый -- не чаще раза в PerTunnel. Не в цикле проверок: бюджет проверки
// 10 с, а замер -- секунды плюс внешние запросы.
type Prober struct {
	Client    *awgmgr.Client
	Now       func() time.Time
	Every     time.Duration
	PerTunnel time.Duration
	// Own -- свой замер (в тестах подменяется); nil -- OwnMeasure.
	Own func(ctx context.Context, iface string) (vpnIP, directIP string, err error)

	mu        sync.Mutex
	results   map[string]wire.ExitProbe
	current   map[string]bool
	awgmFails int
	awgmOff   time.Time
}

func (p *Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC()
}

func (p *Prober) perTunnel() time.Duration {
	if p.PerTunnel > 0 {
		return p.PerTunnel
	}
	return defaultPerTunnel
}

// Running сообщает, работает ли туннель: включён и опубликовал интерфейс, а
// статус не говорит обратного. Общая с Task 15 (использующим её позже)
// проверка живёт здесь одна, без дублей.
func Running(t awgmgr.Tunnel) bool {
	if !t.Enabled || strings.TrimSpace(t.InterfaceName) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(t.Status)) {
	case "", "running", "up", "started", "active", "connected":
		return true
	}
	return false
}

// Step -- один замер: самый давно не мерянный работающий VPN-туннель, если его
// срок подошёл. Возвращает id измеренного или "".
func (p *Prober) Step(ctx context.Context) string {
	ta, err := p.Client.TunnelsAll(ctx)
	if err != nil {
		return ""
	}
	now := p.now()
	var due *awgmgr.Tunnel
	var dueAt time.Time
	p.mu.Lock()
	if p.results == nil {
		p.results = map[string]wire.ExitProbe{}
	}
	p.current = map[string]bool{}
	for i := range ta.Tunnels {
		t := ta.Tunnels[i]
		p.current[t.ID] = true
		if !Running(t) {
			continue
		}
		last := p.results[t.ID].At
		if !last.IsZero() && now.Sub(last) < p.perTunnel() {
			continue
		}
		if due == nil || last.Before(dueAt) {
			due, dueAt = &ta.Tunnels[i], last
		}
	}
	p.mu.Unlock()
	if due == nil {
		return ""
	}
	res := p.measure(ctx, *due)
	p.mu.Lock()
	p.results[due.ID] = res
	p.mu.Unlock()
	return due.ID
}

// ProbeNow -- замер по кнопке. Остановленный VPN-туннель -- ошибка: мерить нечем.
func (p *Prober) ProbeNow(ctx context.Context, tunnelID string) (wire.ExitProbe, error) {
	ta, err := p.Client.TunnelsAll(ctx)
	if err != nil {
		return wire.ExitProbe{}, fmt.Errorf("exit_ip_probe: %w", err)
	}
	for _, t := range ta.Tunnels {
		if t.ID != tunnelID {
			continue
		}
		if !Running(t) {
			return wire.ExitProbe{}, fmt.Errorf("exit_ip_probe: VPN-туннель %s не работает -- мерить нечего", tunnelID)
		}
		res := p.measure(ctx, t)
		p.mu.Lock()
		if p.results == nil {
			p.results = map[string]wire.ExitProbe{}
		}
		p.results[t.ID] = res
		p.mu.Unlock()
		return res, nil
	}
	return wire.ExitProbe{}, fmt.Errorf("exit_ip_probe: VPN-туннеля %s на роутере нет", tunnelID)
}

func (p *Prober) awgmAllowed(now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.awgmOff.IsZero() || !now.Before(p.awgmOff)
}

func (p *Prober) awgmResult(now time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case err == nil:
		p.awgmFails, p.awgmOff = 0, time.Time{}
	case errors.Is(err, awgmgr.ErrUnsupportedByRouter):
		p.awgmFails, p.awgmOff = 0, now.Add(awgmPause)
	default:
		p.awgmFails++
		if p.awgmFails >= 2 {
			p.awgmFails, p.awgmOff = 0, now.Add(awgmPause)
		}
	}
}

func (p *Prober) measure(ctx context.Context, t awgmgr.Tunnel) wire.ExitProbe {
	now := p.now()
	if p.awgmAllowed(now) {
		ip, err := p.Client.TestIP(ctx, t.ID)
		p.awgmResult(now, err)
		if err == nil {
			res := wire.ExitProbe{VPNIP: ip.VPNIP, DirectIP: ip.DirectIP, EndpointIP: ip.EndpointIP, Source: wire.ExitSourceAwgm, At: now}
			if ip.VPNIP == "" || ip.DirectIP == "" {
				res.Err = "awg-manager не назвал оба адреса"
				return res
			}
			changed := ip.IPChanged
			res.Changed = &changed
			return res
		}
	}
	own := p.Own
	if own == nil {
		own = OwnMeasure
	}
	vpn, direct, err := own(ctx, strings.TrimSpace(t.InterfaceName))
	res := wire.ExitProbe{VPNIP: vpn, DirectIP: direct, Source: wire.ExitSourceAgent, At: now}
	if err != nil {
		res.Err = wire.ClipText(err.Error())
	}
	if vpn != "" && direct != "" {
		changed := vpn != direct
		res.Changed = &changed
	}
	return res
}

// Snapshot -- блок фактов: только VPN-туннели последнего инвентаря.
func (p *Prober) Snapshot() *wire.ExitFacts {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.results) == 0 {
		return nil
	}
	out := &wire.ExitFacts{Tunnels: map[string]wire.ExitProbe{}}
	for id, r := range p.results {
		if p.current != nil && !p.current[id] {
			continue
		}
		out.Tunnels[id] = r
		if r.At.After(out.At) {
			out.At = r.At
		}
	}
	if len(out.Tunnels) == 0 {
		return nil
	}
	return out
}

// Run -- замер раз в Every до отмены ctx.
func (p *Prober) Run(ctx context.Context) {
	every := p.Every
	if every <= 0 {
		every = defaultEvery
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(firstDelay):
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		p.Step(sctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
