package checks

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestAwgHandshake(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name    string
		stdout  string
		runErr  error
		want    string
		wantErr string
	}{
		{
			name:   "fresh single peer",
			stdout: "abcdEF=\t" + itoa(now-30) + "\n",
			want:   "ok",
		},
		{
			name:   "stale single peer",
			stdout: "abcdEF=\t" + itoa(now-3600) + "\n",
			want:   "fail",
		},
		{
			name:   "never handshaked (0)",
			stdout: "abcdEF=\t0\n",
			want:   "fail",
		},
		{
			name:   "two peers one fresh",
			stdout: "aa==\t0\nbb==\t" + itoa(now-10) + "\n",
			want:   "ok",
		},
		{
			name:    "wg binary missing",
			runErr:  errors.New("exec: \"wg\": executable file not found"),
			want:    "fail",
			wantErr: "wg show failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Deps{Runner: RunnerFunc(func(_ context.Context, name string, args ...string) (string, error) {
				if name != "wg" {
					t.Fatalf("unexpected exec %s", name)
				}
				return c.stdout, c.runErr
			})}
			chk := AwgHandshake{Iface: "awg0", MaxAge: 180 * time.Second}
			got := chk.Run(context.Background(), d)
			if got.Status != c.want {
				t.Fatalf("status=%s want=%s details=%v", got.Status, c.want, got.Details)
			}
			if c.wantErr != "" && got.Details["error"] != c.wantErr {
				t.Fatalf("error=%v want=%q", got.Details["error"], c.wantErr)
			}
		})
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
