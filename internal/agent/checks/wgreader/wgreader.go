// Package wgreader abstracts how the agent reads WireGuard peer state
// (specifically the latest handshake timestamp).
//
// Why: the original implementation shelled out to `wg show <iface>
// latest-handshakes`. That binary is missing on routers like Keenetic OS
// (where `awg` from the awg-manager package replaces it), and the agent
// must work out-of-box without manual setup. This package provides
// multi-strategy auto-detection at agent startup.
//
// Strategy order:
//
//  1. wgctrl  — pure-Go netlink (kernel WG) plus userspace UNIX-socket
//               fallback at /var/run/wireguard/<iface>.sock (covers
//               wireguard-go, BoringTun, amneziawg-go).
//  2. shell   — `<binary> show <iface> latest-handshakes`. Probes for
//               /opt/sbin/awg → awg → wg in that order; first found wins.
//
// The `Multi` reader tries each in order. If one returns ErrNoPeers (the
// iface exists but no peer has ever handshook) we treat that as the
// authoritative answer — falling through could mask reality.
package wgreader

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
)

// Reader is the interface for reading WireGuard peer state.
type Reader interface {
	LatestHandshakeAge(ctx context.Context, iface string) (time.Duration, error)
	Name() string
}

// CmdRunner mirrors checks.Runner — redeclared here to keep wgreader a
// leaf package without an import cycle on `checks`.
type CmdRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// ErrNoPeers indicates the iface exists but no peer has ever handshook.
// Authoritative — Multi short-circuits on this and does not try further readers.
var ErrNoPeers = errors.New("no peer ever handshook")

// ErrNoStrategies means none of the configured readers could be initialized.
var ErrNoStrategies = errors.New("no working WG reader strategy")

// Multi tries a list of readers in order and returns the first successful result.
type Multi struct {
	readers []Reader
}

func (m *Multi) Name() string { return "multi" }

// LatestHandshakeAge tries each reader in order. Returns on first success.
// Short-circuits on ErrNoPeers (authoritative "iface up, no peer ever handshook").
func (m *Multi) LatestHandshakeAge(ctx context.Context, iface string) (time.Duration, error) {
	if len(m.readers) == 0 {
		return 0, ErrNoStrategies
	}
	var errs []string
	for _, r := range m.readers {
		age, err := r.LatestHandshakeAge(ctx, iface)
		if err == nil {
			return age, nil
		}
		if errors.Is(err, ErrNoPeers) {
			return 0, err
		}
		errs = append(errs, fmt.Sprintf("%s: %v", r.Name(), err))
	}
	return 0, fmt.Errorf("all wg readers failed: %s", strings.Join(errs, "; "))
}

// Strategies lists reader names in probe order — useful for startup diagnostics.
func (m *Multi) Strategies() []string {
	out := make([]string, len(m.readers))
	for i, r := range m.readers {
		out[i] = r.Name()
	}
	return out
}

// Detect probes the system at startup. wgctrl tried first (no binary needed);
// then a single shell fallback picked from {/opt/sbin/awg, awg, wg}.
func Detect(runner CmdRunner) *Multi {
	m := &Multi{}
	if r, err := newWgctrlReader(); err == nil {
		m.readers = append(m.readers, r)
	}
	for _, candidate := range []string{"/opt/sbin/awg", "awg", "wg"} {
		if path, err := exec.LookPath(candidate); err == nil {
			m.readers = append(m.readers, newShellReader(runner, path))
			break
		}
	}
	return m
}

// --- wgctrl-based reader -----------------------------------------------------

type wgctrlReader struct {
	client *wgctrl.Client
}

func newWgctrlReader() (*wgctrlReader, error) {
	c, err := wgctrl.New()
	if err != nil {
		return nil, fmt.Errorf("wgctrl.New: %w", err)
	}
	return &wgctrlReader{client: c}, nil
}

func (w *wgctrlReader) Name() string { return "wgctrl" }

func (w *wgctrlReader) LatestHandshakeAge(ctx context.Context, iface string) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	dev, err := w.client.Device(iface)
	if err != nil {
		return 0, fmt.Errorf("device %q: %w", iface, err)
	}
	var freshest time.Duration = -1
	for _, p := range dev.Peers {
		if p.LastHandshakeTime.IsZero() {
			continue
		}
		age := time.Since(p.LastHandshakeTime)
		if age < 0 {
			age = 0
		}
		if freshest < 0 || age < freshest {
			freshest = age
		}
	}
	if freshest < 0 {
		return 0, ErrNoPeers
	}
	return freshest, nil
}

// --- shell-based reader ------------------------------------------------------

type shellReader struct {
	runner CmdRunner
	bin    string
}

func newShellReader(runner CmdRunner, bin string) *shellReader {
	return &shellReader{runner: runner, bin: bin}
}

func (s *shellReader) Name() string { return "shell:" + s.bin }

func (s *shellReader) LatestHandshakeAge(ctx context.Context, iface string) (time.Duration, error) {
	out, err := s.runner.Run(ctx, s.bin, "show", iface, "latest-handshakes")
	if err != nil {
		return 0, fmt.Errorf("%s show: %w (stderr: %q)", s.bin, err, strings.TrimSpace(out))
	}
	now := time.Now().Unix()
	var freshest int64 = -1
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
		if freshest < 0 || age < freshest {
			freshest = age
		}
	}
	if freshest < 0 {
		return 0, ErrNoPeers
	}
	return time.Duration(freshest) * time.Second, nil
}
