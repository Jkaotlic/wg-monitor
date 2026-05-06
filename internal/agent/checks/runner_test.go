package checks

import (
	"context"
	"strings"
	"testing"
)

func TestOSExecRunsTrue(t *testing.T) {
	r := OSExec{}
	out, err := r.Run(context.Background(), "go", "version")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "go") {
		t.Fatalf("want 'go' in output, got %q", out)
	}
}

func TestRunnerFunc(t *testing.T) {
	r := RunnerFunc(func(ctx context.Context, name string, args ...string) (string, error) {
		return name + " " + strings.Join(args, "|"), nil
	})
	got, _ := r.Run(context.Background(), "wg", "show", "awg0")
	if got != "wg show|awg0" {
		t.Fatalf("got %q", got)
	}
}
