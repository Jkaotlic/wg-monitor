package alerts

import (
	"strings"
	"testing"
)

func TestRenderDeferredUpdateSuccess(t *testing.T) {
	card := RenderDeferredUpdate("route4car4", "v0.13.0-rc53", "ok", "")
	text := card.Render(CardOpts{MaxBytes: 1200})
	for _, want := range []string{"route4car4", "отложенное обновление", "v0.13.0-rc53", "heartbeat"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered success missing %q: %q", want, text)
		}
	}
}

func TestRenderDeferredUpdateFailure(t *testing.T) {
	card := RenderDeferredUpdate("route4car4", "v0.13.0-rc53", "err", "роутер не смог скачать обновление")
	text := card.Render(CardOpts{MaxBytes: 1200})
	for _, want := range []string{"route4car4", "не ставится", "v0.13.0-rc53", "роутер не смог скачать обновление", "«Парк»"} {
		if !strings.Contains(text, want) {
			t.Fatalf("rendered failure missing %q: %q", want, text)
		}
	}
	for _, banned := range []string{"pending", "update", "self_update", "попробует снова"} {
		if strings.Contains(text, banned) {
			t.Fatalf("failure text contains %q: %q", banned, text)
		}
	}
}
