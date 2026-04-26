package checks

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type AwgHandshake struct {
	Iface  string
	MaxAge time.Duration
}

func (AwgHandshake) Name() string { return "awg_handshake" }

func (c AwgHandshake) Run(ctx context.Context, d Deps) wire.Check {
	start := time.Now()
	out, err := d.Runner.Run(ctx, "wg", "show", c.Iface, "latest-handshakes")
	if err != nil {
		return Fail(c.Name(), start, "wg show failed", map[string]any{"stderr": strings.TrimSpace(out)})
	}
	now := time.Now().Unix()
	freshest := int64(-1)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ts, perr := strconv.ParseInt(fields[1], 10, 64)
		if perr != nil || ts == 0 {
			continue
		}
		age := now - ts
		if freshest == -1 || age < freshest {
			freshest = age
		}
	}
	if freshest == -1 {
		return Fail(c.Name(), start, "no peer ever handshook", nil)
	}
	if time.Duration(freshest)*time.Second > c.MaxAge {
		return Fail(c.Name(), start, "stale", map[string]any{"handshake_age_sec": freshest})
	}
	return OK(c.Name(), start, map[string]any{"handshake_age_sec": freshest})
}
