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
	}
}

func (l *Loop) Run(ctx context.Context) {
	attempt := 0
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
			slog.Warn("cmdloop poll failed, backing off", "err", err, "wait", wait, "attempt", attempt)
			if !sleepCtx(ctx, wait) {
				return
			}
			continue
		}
		attempt = 0
		if cmd == nil {
			// 204 idle — go right back to long-poll.
			continue
		}
		slog.Info("cmdloop received command", "cmd_id", cmd.ID, "action", cmd.Action)
		res := l.runner.Execute(ctx, *cmd)
		if perr := l.client.PostResult(ctx, res); perr != nil {
			slog.Warn("cmdloop post result failed (continuing)", "cmd_id", cmd.ID, "err", perr)
		}
	}
}

func (l *Loop) backoff(attempt int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt-1))) * l.BackoffBase
	if d > l.BackoffMax {
		d = l.BackoffMax
	}
	return d
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
