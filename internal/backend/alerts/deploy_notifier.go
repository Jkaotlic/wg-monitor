package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
)

// DeployNotifier posts backend/VPS-mediated agent update outcomes to the
// router's people. It is intentionally separate from command-result replies:
// deferred jobs often have no Telegram message to reply to.
type DeployNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	notify notifySink
}

func NewDeployNotifier(d *db.DB, tgc LifecycleSendTG, adminID int64) *DeployNotifier {
	return &DeployNotifier{
		db: d, tg: tgc,
		notify: notify.NewFanout(d, tgc, slog.Default(), adminID),
	}
}

// SetNotifySink подменяет рассылку. Только для тестов.
func (n *DeployNotifier) SetNotifySink(s notifySink) { n.notify = s }

func (n *DeployNotifier) SendDeferredUpdate(ctx context.Context, userID int64, nickname, targetVersion, status, output string) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("deploy notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	card := RenderDeferredUpdate(nickname, targetVersion, status, output)
	text := card.Render(CardOpts{MaxBytes: 1200})
	if _, err := n.notify.Send(ctx, userID, text, ""); err != nil {
		slog.Warn("deploy notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
		return err
	}
	return nil
}

func RenderDeferredUpdate(nickname, targetVersion, status, output string) Card {
	targetVersion = strings.TrimSpace(targetVersion)
	if targetVersion == "" {
		targetVersion = "целевая версия неизвестна"
	}
	if status == "ok" {
		return Card{
			Badge:   "⬆️✅",
			Summary: fmt.Sprintf("%s: отложенное обновление агента применено", nickname),
			Meta:    []string{KV("версия", targetVersion), KV("подтверждение", "heartbeat с новой версией")},
		}
	}
	reason := strings.TrimSpace(output)
	if reason == "" {
		reason = "агент не сообщил причину"
	}
	return Card{
		Badge:   "⬆️⚠️",
		Summary: fmt.Sprintf("Обновление агента на %s не ставится: %s", nickname, truncateDeployOutput(reason, 300)),
		Meta:    []string{KV("версия", targetVersion)},
		Hint:    "Попытки прекращены. Назначить заново или посмотреть причину — в приложении, экран «Парк».",
	}
}

func truncateDeployOutput(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max < 4 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
