package alerts

import (
	"context"
	"errors"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestFormatUnstickFixed(t *testing.T) {
	const (
		restarted = "зависла в awg-manager — перезапустил, работает"
		started   = "была включена, но не запустилась — запустил, работает"
		stopped   = "была выключена, но не останавливалась — остановил"
	)
	cases := []struct {
		from  string
		steps []string
		to    string
		want  string
	}{
		{"broken", []string{"restart"}, "running", restarted},
		{"starting", []string{"restart"}, "running", restarted},
		{"needs_start", []string{"start"}, "running", started},
		{"needs_stop", []string{"stop"}, "stopped", stopped},
		{"needs_stop", []string{"start"}, "running", started},
		{"stopping", []string{"restart", "service_restart"}, "running", restarted},
		{"broken", []string{"stop"}, "stopped", stopped},
		{"starting", []string{"stop"}, "stopped", stopped},
		{"broken", []string{"stop"}, "disabled", stopped},
		{"broken", []string{"stop"}, "not_created", stopped},
		{"broken", []string{"restart"}, "weird", "зависла в awg-manager — вывел из зависания"},
	}
	for _, c := range cases {
		got := FormatUnstickFixed("home", wire.UnstickEvent{TunnelID: "nwg0", TunnelName: "Франкфурт", From: c.from, Steps: c.steps, To: c.to, Result: wire.UnstickFixed})
		if !strings.Contains(got, "«Франкфурт»") || !strings.Contains(got, c.want) || !strings.Contains(got, "home") {
			t.Errorf("%s: %q", c.from, got)
		}
		for _, bad := range []string{"broken", "needs_start", "needs_stop", "starting", "stopping", "awgm_unstick", "service_restart", "gave_up"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: служебное слово %q в тексте владельцу: %q", c.from, bad, got)
			}
		}
	}
	svc := FormatUnstickFixed("home", wire.UnstickEvent{TunnelID: "nwg0", From: "broken", Steps: []string{"restart", "service_restart"}, To: "running", Result: wire.UnstickFixed})
	if !strings.Contains(svc, "Пришлось перезапустить awg-manager целиком") || !strings.Contains(svc, "«nwg0»") || strings.Contains(svc, "service_restart") {
		t.Errorf("service/no name: %q", svc)
	}
}

type failFirstSender struct{ calls, ok int }

func (f *failFirstSender) SendSilentKeyboard(_ context.Context, _ int64, _, _ string, _ *tg.InlineKeyboardMarkup) (int, error) {
	f.calls++
	if f.calls == 1 {
		return 0, errors.New("boom")
	}
	f.ok++
	return 1, nil
}

func TestSendUnstick_ContinuesAfterError(t *testing.T) {
	s := &failFirstSender{}
	ev := func(id string) wire.UnstickEvent {
		return wire.UnstickEvent{ID: id, TunnelID: "nwg0", From: "broken", Result: wire.UnstickFixed}
	}
	err := NewUnstickNotifier(s).SendUnstick(context.Background(), 1, "home", []wire.UnstickEvent{ev("a"), ev("b")})
	if err == nil || s.calls != 2 || s.ok != 1 {
		t.Fatalf("err=%v calls=%d ok=%d", err, s.calls, s.ok)
	}
}
