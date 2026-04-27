package checks

import (
	"context"
	"errors"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/checks/wgreader"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type AwgHandshake struct {
	Iface  string
	MaxAge time.Duration
}

func (AwgHandshake) Name() string { return "awg_handshake" }

func (c AwgHandshake) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	if d.WGReader == nil {
		return Fail(c.Name(), start, "no WG reader configured", nil)
	}
	age, err := d.WGReader.LatestHandshakeAge(ctx, c.Iface)
	if err != nil {
		if errors.Is(err, wgreader.ErrNoPeers) {
			return Fail(c.Name(), start, "no peer ever handshook", nil)
		}
		return Fail(c.Name(), start, "wg read failed", map[string]any{"detail": err.Error()})
	}
	secs := int64(age.Round(time.Second).Seconds())
	if age > c.MaxAge {
		return Fail(c.Name(), start, "stale", map[string]any{"handshake_age_sec": secs})
	}
	return OK(c.Name(), start, map[string]any{"handshake_age_sec": secs})
}
