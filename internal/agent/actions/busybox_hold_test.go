package actions

import (
	"context"
	"errors"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBusyboxHold(t *testing.T) {
	for _, tc := range []struct {
		name, listing string
		fail          string
		want          string
	}{
		{"verified", "Package: busybox\nVersion: 1.37.0-4\nStatus: install hold installed\n", "", "ok"},
		{"not held", "Package: busybox\nStatus: install ok installed\n", "", "err"},
		{"multiple flags", "Package: busybox\nStatus: install hold,user installed\n", "", "ok"},
		{"invalid dpkg shape", "Package: busybox\nStatus: hold ok installed\n", "", "err"},
		{"missing", "", "", "err"},
		{"wrong package", "Package: busybox-extras\nStatus: install hold installed\n", "", "err"},
		{"not installed", "Package: busybox\nStatus: install hold not-installed\n", "", "err"},
		{"write failed", "", "flag", "err"},
		{"read failed", "", "status", "err"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			o := mkOpkgRunner(t, func(_ context.Context, name string, args ...string) ([]byte, error) {
				call := name + " " + strings.Join(args, " ")
				calls = append(calls, call)
				if len(args) == 0 || name != "opkg" {
					t.Fatalf("unexpected call: %s", call)
				}
				if args[0] == tc.fail {
					return nil, errors.New("failure")
				}
				if args[0] == "status" {
					return []byte(tc.listing), nil
				}
				return nil, nil
			})
			s, out := o.HoldBusybox(context.Background())
			if s != tc.want {
				t.Fatalf("status=%s output=%s", s, out)
			}
			if len(calls) == 0 || calls[0] != "opkg flag hold busybox" {
				t.Fatalf("calls=%v", calls)
			}
			if tc.fail != "flag" && (len(calls) != 2 || calls[1] != "opkg status busybox") {
				t.Fatalf("calls=%v", calls)
			}
			if _, err := os.Stat(o.LockPath); !os.IsNotExist(err) {
				t.Fatalf("lock leaked: %v", err)
			}
		})
	}
}

func TestBusyboxHoldRefusesBusyLock(t *testing.T) {
	o := mkOpkgRunner(t, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("executed while locked")
		return nil, nil
	})
	if err := os.WriteFile(o.LockPath, []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	s, _ := o.HoldBusybox(context.Background())
	if s != "locked" {
		t.Fatal(s)
	}
}

func TestBusyboxHoldUsesCronLockAndRunner(t *testing.T) {
	o := mkOpkgRunner(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "status" {
			return []byte("Package: busybox\nStatus: install hold installed\n"), nil
		}
		return nil, nil
	})
	o.SharedLockDir = filepath.Join(t.TempDir(), "cron.lock")
	if err := os.Mkdir(o.SharedLockDir, 0700); err != nil {
		t.Fatal(err)
	}
	// A fresh lock with this live process is a real cron/agent conflict.
	if err := os.WriteFile(filepath.Join(o.SharedLockDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Opkg: o}
	res := r.Execute(context.Background(), wire.Command{ID: "hold", Action: "opkg_busybox_hold"})
	if res.Status != "locked" {
		t.Fatalf("result=%+v", res)
	}
	if err := os.RemoveAll(o.SharedLockDir); err != nil {
		t.Fatal(err)
	}
	res = r.Execute(context.Background(), wire.Command{ID: "hold2", Action: "opkg_busybox_hold"})
	if res.Status != "ok" {
		t.Fatalf("result=%+v", res)
	}
	if _, err := os.Stat(o.SharedLockDir); !os.IsNotExist(err) {
		t.Fatalf("shared lock leaked: %v", err)
	}
}
