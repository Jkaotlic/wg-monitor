package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent/checks/wgreader"
)

type fakeWGReader struct {
	age time.Duration
	err error
}

func (f *fakeWGReader) Name() string { return "fake" }
func (f *fakeWGReader) LatestHandshakeAge(_ context.Context, _ string) (time.Duration, error) {
	return f.age, f.err
}

func TestAwgHandshake(t *testing.T) {
	cases := []struct {
		name    string
		age     time.Duration
		readErr error
		want    string
		wantErr string
	}{
		{name: "fresh", age: 30 * time.Second, want: "ok"},
		{name: "stale", age: 3600 * time.Second, want: "fail"},
		{name: "no peers", readErr: wgreader.ErrNoPeers, want: "fail", wantErr: "no peer ever handshook"},
		{name: "reader error", readErr: errors.New("netlink: not permitted"), want: "fail", wantErr: "wg read failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Deps{WGReader: &fakeWGReader{age: c.age, err: c.readErr}}
			chk := AwgHandshake{Iface: "wg0", MaxAge: 180 * time.Second}
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

func TestAwgHandshake_NilReader(t *testing.T) {
	chk := AwgHandshake{Iface: "wg0", MaxAge: 180 * time.Second}
	got := chk.Run(context.Background(), Deps{})
	if got.Status != "fail" {
		t.Fatalf("status=%s", got.Status)
	}
}
