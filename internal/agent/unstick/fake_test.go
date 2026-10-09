package unstick

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// fakeAWG -- awg-manager в памяти. onAction решает, во что превращается
// туннель после действия (по умолчанию -- ни во что: остаётся зависшим).
type fakeAWG struct {
	mu         sync.Mutex
	tunnels    []awgmgr.Tunnel
	calls      []string // "restart:nwg0", "start:x", "stop:x"
	readErr    error
	restart404 bool
	onAction   func(action, id string, t *awgmgr.Tunnel)
}

func (f *fakeAWG) TunnelsAll(context.Context) (*awgmgr.TunnelsAll, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &awgmgr.TunnelsAll{Tunnels: append([]awgmgr.Tunnel(nil), f.tunnels...)}, nil
}

func (f *fakeAWG) act(action, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action+":"+id)
	for i := range f.tunnels {
		if f.tunnels[i].ID == id && f.onAction != nil {
			f.onAction(action, id, &f.tunnels[i])
		}
	}
	return nil
}

func (f *fakeAWG) RestartTunnel(_ context.Context, id string) error {
	if f.restart404 {
		f.mu.Lock()
		f.calls = append(f.calls, "restart404:"+id)
		f.mu.Unlock()
		// IsEndpointMissing распознаёт 404 по тексту ошибки клиента
		return errors.New("POST /api/control/restart: HTTP 404")
	}
	return f.act("restart", id)
}
func (f *fakeAWG) StartTunnel(_ context.Context, id string) error { return f.act("start", id) }
func (f *fakeAWG) StopTunnel(_ context.Context, id string) error  { return f.act("stop", id) }

func (f *fakeAWG) set(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tunnels {
		if f.tunnels[i].ID == id {
			f.tunnels[i].Status = status
		}
	}
}

func (f *fakeAWG) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// clock -- поддельное время; Sleep двигает его, не засыпая.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }
func (c *clock) sleep(_ context.Context, d time.Duration) error {
	c.t = c.t.Add(d)
	return nil
}
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type harness struct {
	awg      *fakeAWG
	clk      *clock
	w        *Watcher
	services int
}

func newHarness(tunnels ...awgmgr.Tunnel) *harness {
	h := &harness{
		awg: &fakeAWG{tunnels: tunnels},
		clk: &clock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)},
	}
	h.w = New(Config{Enabled: true}, Deps{
		AWG:            h.awg,
		RestartService: func(context.Context) error { h.services++; return nil },
		Now:            h.clk.now,
		Sleep:          h.clk.sleep,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return h
}

func tun(id, status string, enabled bool) awgmgr.Tunnel {
	return awgmgr.Tunnel{ID: id, Name: "line-" + id, Status: status, Enabled: enabled}
}
