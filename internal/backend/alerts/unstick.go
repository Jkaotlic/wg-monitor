package alerts

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// UnstickSender -- notify.Fanout.
type UnstickSender interface {
	SendSilentKeyboard(ctx context.Context, routerUserID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) (int, error)
}

// UnstickNotifier шлёт владельцу беззвучное «линия зависла — вывел».
type UnstickNotifier struct{ s UnstickSender }

func NewUnstickNotifier(s UnstickSender) *UnstickNotifier { return &UnstickNotifier{s: s} }

func (n *UnstickNotifier) SendUnstick(ctx context.Context, userID int64, nickname string, events []wire.UnstickEvent) error {
	for _, ev := range events {
		if ev.Result != wire.UnstickFixed {
			continue
		}
		if _, err := n.s.SendSilentKeyboard(ctx, userID, FormatUnstickFixed(nickname, ev), "", nil); err != nil {
			return err
		}
	}
	return nil
}

// FormatUnstickFixed -- текст владельцу. Без служебных слов awg-manager
// (alerts-speak-to-owner): только что было и что сделано.
func FormatUnstickFixed(nickname string, ev wire.UnstickEvent) string {
	name := strings.TrimSpace(ev.TunnelName)
	if name == "" {
		name = ev.TunnelID
	}
	var what string
	switch ev.From {
	case "needs_start":
		what = "была включена, но не запустилась — запустил, работает."
	case "needs_stop", "stopping":
		what = "была выключена, но продолжала работать — остановил."
	default: // broken, starting
		what = "зависла в awg-manager — перезапустил, работает."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🔧 %s\nЛиния «%s» %s", nickname, name, what)
	if slices.Contains(ev.Steps, "service_restart") {
		b.WriteString("\nПришлось перезапустить awg-manager целиком.")
	}
	return b.String()
}
