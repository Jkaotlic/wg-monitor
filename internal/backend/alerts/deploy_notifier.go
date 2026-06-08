package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/anex/wg-monitor/internal/backend/db"
)

// DeployNotifier posts backend/VPS-mediated agent update outcomes into the
// router topic. It is intentionally separate from command-result replies:
// deferred jobs often have no Telegram message to reply to.
type DeployNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	chatID int64
}

func NewDeployNotifier(d *db.DB, tg LifecycleSendTG, chatID int64) *DeployNotifier {
	return &DeployNotifier{db: d, tg: tg, chatID: chatID}
}

func (n *DeployNotifier) SendDeferredUpdate(ctx context.Context, userID int64, nickname, targetVersion, status, output string) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("deploy notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	if user.TelegramThreadID == nil {
		slog.Debug("deploy notifier: no thread, skipping", "user_id", userID, "nickname", nickname)
		return nil
	}
	card := RenderDeferredUpdate(nickname, targetVersion, status, output)
	text := card.Render(CardOpts{MaxBytes: 1200})
	_, err = n.tg.SendMessage(ctx, user.EffectiveTelegramChatID(n.chatID), user.TelegramThreadID, text, "", nil)
	if err != nil {
		slog.Warn("deploy notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
	}
	return err
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
	output = strings.TrimSpace(output)
	if output == "" {
		output = "агент вернул статус " + strings.TrimSpace(status)
	}
	return Card{
		Badge:   "⬆️⚠️",
		Summary: fmt.Sprintf("%s: отложенное обновление агента не применилось", nickname),
		Meta:    []string{KV("цель", targetVersion), KV("статус", strings.TrimSpace(status))},
		Details: truncateDeployOutput(output, 700),
		Hint:    "VPS оставит задачу в ожидании и попробует снова, когда агент снова будет на связи.",
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
