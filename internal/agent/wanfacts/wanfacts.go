// Package wanfacts -- подключения к провайдеру (основное и резервные) из
// /api/wan/status awg-manager (сверка С1). Роль задаёт priority: больше --
// выше, 0 -- подключение в выборе выхода не участвует.
package wanfacts

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const defaultEvery = 10 * time.Minute

// Links раскладывает интерфейсы по ролям.
func Links(st *awgmgr.WANStatus) []wire.WANLink {
	if st == nil {
		return nil
	}
	var out []wire.WANLink
	for name, it := range st.Interfaces {
		if it.Priority <= 0 {
			continue
		}
		out = append(out, wire.WANLink{Name: name, Label: strings.TrimSpace(it.Label), Up: it.Up, Priority: it.Priority})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		out[i].Role = "backup"
		if i == 0 {
			out[i].Role = "primary"
		}
	}
	return out
}

// Collector отдаёт блок WAN не чаще раза в Every.
type Collector struct {
	Client *awgmgr.Client
	Now    func() time.Time
	Every  time.Duration
	// PingCheck -- профиль Ping-Check по linux-имени интерфейса ("" -- нет
	// профиля). Появится после сверки С3 (Task 15); nil -- о Ping-Check молчим.
	PingCheck func(ctx context.Context) (map[string]string, error)

	mu     sync.Mutex
	cached *wire.WANFacts
	at     time.Time
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

func (c *Collector) Fact(ctx context.Context) *wire.WANFacts {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	every := c.Every
	if every <= 0 {
		every = defaultEvery
	}
	if c.cached != nil && now.Sub(c.at) < every {
		return c.cached
	}
	f := &wire.WANFacts{At: now}
	st, err := c.Client.WANStatus(ctx)
	switch {
	case errors.Is(err, awgmgr.ErrUnsupportedByRouter):
		f.Unsupported = true
	case err != nil:
		f.Err = err.Error()
	default:
		f.Links = Links(st)
		if c.PingCheck != nil {
			if profiles, perr := c.PingCheck(ctx); perr == nil {
				for i := range f.Links {
					if p, ok := profiles[f.Links[i].Name]; ok {
						p := p
						f.Links[i].PingCheck = &p
					}
				}
			}
		}
	}
	c.cached, c.at = f, now
	return f
}
