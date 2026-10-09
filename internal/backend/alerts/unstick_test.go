package alerts

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestFormatUnstickFixed(t *testing.T) {
	cases := []struct {
		from  string
		steps []string
		want  string
	}{
		{"broken", []string{"restart"}, "зависла в awg-manager — перезапустил, работает"},
		{"starting", []string{"restart"}, "зависла в awg-manager — перезапустил, работает"},
		{"needs_start", []string{"start"}, "была включена, но не запустилась — запустил, работает"},
		{"needs_stop", []string{"stop"}, "была выключена, но продолжала работать — остановил"},
		{"stopping", []string{"stop"}, "была выключена, но продолжала работать — остановил"},
	}
	for _, c := range cases {
		got := FormatUnstickFixed("home", wire.UnstickEvent{TunnelID: "nwg0", TunnelName: "Франкфурт", From: c.from, Steps: c.steps, Result: wire.UnstickFixed})
		if !strings.Contains(got, "«Франкфурт»") || !strings.Contains(got, c.want) || !strings.Contains(got, "home") {
			t.Errorf("%s: %q", c.from, got)
		}
		for _, bad := range []string{"broken", "needs_start", "needs_stop", "starting", "stopping", "awgm_unstick", "service_restart", "gave_up"} {
			if strings.Contains(got, bad) {
				t.Errorf("%s: служебное слово %q в тексте владельцу: %q", c.from, bad, got)
			}
		}
	}
	svc := FormatUnstickFixed("home", wire.UnstickEvent{TunnelID: "nwg0", From: "broken", Steps: []string{"restart", "service_restart"}, Result: wire.UnstickFixed})
	if !strings.Contains(svc, "Пришлось перезапустить awg-manager целиком") || !strings.Contains(svc, "«nwg0»") || strings.Contains(svc, "service_restart") {
		t.Errorf("service/no name: %q", svc)
	}
}
