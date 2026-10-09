package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/unstick"
)

func TestBuildUnstick(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &agent.Config{}
	w, c := buildUnstick(cfg, awgmgr.New("http://127.0.0.1:1"), nil, log)
	if w == nil || c == nil || c.Name() != unstick.CheckName {
		t.Fatalf("default: w=%v c=%v", w, c)
	}
	if ch, ok := c.(unstick.Check); !ok || ch.Disabled || ch.Source == nil {
		t.Errorf("default check: %#v", c)
	}
	off := false
	cfg.Unstick.Enabled = &off
	w, c = buildUnstick(cfg, awgmgr.New("http://127.0.0.1:1"), nil, log)
	if w != nil {
		t.Error("disabled config still built a watcher")
	}
	if ch, ok := c.(unstick.Check); !ok || !ch.Disabled {
		t.Errorf("disabled check: %#v", c)
	}
}
