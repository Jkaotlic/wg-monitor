// Package cmdloop is the agent-side long-poll loop for the command channel.
//
// One Run goroutine per agent process: poll /v1/cmd, hand any returned
// wire.Command to a Runner, post the result, repeat. Errors trigger an
// exponential backoff capped at BackoffMax. ctx-cancel exits cleanly.
package cmdloop

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// BackendClient is the subset of agent.Client used by the loop.
type BackendClient interface {
	PollCommand(ctx context.Context, waitSec int) (*wire.Command, error)
	PostResult(ctx context.Context, result wire.CommandResult) error
}

// CommandRunner is satisfied by *actions.Runner. Decoupled so the loop's
// tests don't need a real awg-manager / opkg.
type CommandRunner interface {
	Execute(ctx context.Context, cmd wire.Command) wire.CommandResult
}

type Loop struct {
	client      BackendClient
	runner      CommandRunner
	WaitSec     int
	BackoffBase time.Duration
	BackoffMax  time.Duration
	randFloat64 func() float64
	resultCache map[string]cachedResult
	cacheTTL    time.Duration
	cacheMax    int
}

type cachedResult struct {
	result wire.CommandResult
	at     time.Time
}

func New(client BackendClient, runner CommandRunner, waitSec int) *Loop {
	if waitSec <= 0 {
		waitSec = 30
	}
	return &Loop{
		client:      client,
		runner:      runner,
		WaitSec:     waitSec,
		BackoffBase: 1 * time.Second,
		BackoffMax:  60 * time.Second,
		randFloat64: rand.Float64,
		resultCache: make(map[string]cachedResult),
		cacheTTL:    10 * time.Minute,
		cacheMax:    128,
	}
}

func (l *Loop) Run(ctx context.Context) {
	attempt := 0
	escalated := false
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		cmd, err := l.client.PollCommand(ctx, l.WaitSec)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			attempt++
			wait := l.backoff(attempt)
			// Escalate to ERROR once after 10 sustained failures so a chronic
			// outage is visible in journalctl without each retry spamming
			// (OBS-12). Reset on next success.
			if attempt >= 10 && !escalated {
				slog.Error("cmdloop chronically failing — backend unreachable", "consecutive_fails", attempt, "err", err)
				escalated = true
			}
			slog.Warn("cmdloop poll failed, backing off", "err", err, "wait", wait, "attempt", attempt)
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		if escalated {
			slog.Info("cmdloop recovered after sustained failure", "previous_consecutive_fails", attempt)
		}
		attempt = 0
		escalated = false
		if cmd == nil {
			// 204 idle — go right back to long-poll.
			continue
		}
		slog.Info("cmdloop received command", "cmd_id", cmd.ID, "action", cmd.Action)
		if cached, ok := l.cachedResult(cmd.ID); ok {
			if perr := l.client.PostResult(ctx, cached); perr != nil {
				slog.Warn("cmdloop post cached result failed (continuing)", "cmd_id", cmd.ID, "err", perr)
			}
			continue
		}
		res := l.runner.Execute(ctx, *cmd)
		l.rememberResult(cmd.ID, res)
		if perr := l.client.PostResult(ctx, res); perr != nil {
			slog.Warn("cmdloop post result failed (continuing)", "cmd_id", cmd.ID, "err", perr)
		}
	}
}

func (l *Loop) cachedResult(id string) (wire.CommandResult, bool) {
	if id == "" || l.resultCache == nil {
		return wire.CommandResult{}, false
	}
	cached, ok := l.resultCache[id]
	if !ok {
		return wire.CommandResult{}, false
	}
	if l.cacheTTL > 0 && time.Since(cached.at) > l.cacheTTL {
		delete(l.resultCache, id)
		return wire.CommandResult{}, false
	}
	return cached.result, true
}

func (l *Loop) rememberResult(id string, result wire.CommandResult) {
	if id == "" {
		return
	}
	if l.resultCache == nil {
		l.resultCache = make(map[string]cachedResult)
	}
	if l.cacheMax <= 0 {
		l.cacheMax = 128
	}
	if len(l.resultCache) >= l.cacheMax {
		var oldestID string
		var oldestAt time.Time
		for id, cached := range l.resultCache {
			if oldestID == "" || cached.at.Before(oldestAt) {
				oldestID = id
				oldestAt = cached.at
			}
		}
		delete(l.resultCache, oldestID)
	}
	l.resultCache[id] = cachedResult{result: result, at: time.Now()}
}

func (l *Loop) backoff(attempt int) time.Duration {
	// Clamp attempt PRE-Pow: math.Pow(2, ~63) уже превышает float64 mantissa
	// precision, ~1024 даёт +Inf. time.Duration(int64) от +Inf — undefined,
	// в 1.21+ становится отрицательным → time.NewTimer(-d) срабатывает
	// мгновенно → busy-loop при долгих оффлайнах (BUG-05). 30 шагов хватает
	// чтобы уверенно перешагнуть BackoffMax при любом разумном BackoffBase.
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 30 {
		attempt = 30
	}
	d := time.Duration(math.Pow(2, float64(attempt-1))) * l.BackoffBase
	if d > l.BackoffMax || d < 0 {
		d = l.BackoffMax
	}
	randFloat64 := l.randFloat64
	if randFloat64 == nil {
		randFloat64 = rand.Float64
	}
	jittered := time.Duration(randFloat64() * float64(d))
	if jittered < time.Millisecond {
		return time.Millisecond
	}
	return jittered
}

// sleepCtx waits d or until ctx done. Returns false if ctx is done first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
