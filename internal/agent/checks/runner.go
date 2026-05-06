package checks

import (
	"context"
	"os/exec"
)

// Runner abstracts subprocess execution so checks are unit-testable
// without a real wg/dig binary on the dev box.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type OSExec struct{}

func (OSExec) Run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

type RunnerFunc func(ctx context.Context, name string, args ...string) (string, error)

func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) (string, error) {
	return f(ctx, name, args...)
}
