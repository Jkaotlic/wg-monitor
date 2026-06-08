package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/pkg/wire"
)

// LifecycleSendTG is the minimal tg.Client surface used by the wake/sleep
// notifiers. Decoupled so tests can fake it.
type LifecycleSendTG interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type lifecycleKeyboardTG interface {
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
}

const mobileWakeCooldown = 5 * time.Minute

// WakeNotifier sends an adaptive 🚗 card to the router's TG-topic when a
// mobile agent's Report carries Resumed=true.
type WakeNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	chatID int64
}

func NewWakeNotifier(d *db.DB, tg LifecycleSendTG, chatID int64) *WakeNotifier {
	return &WakeNotifier{db: d, tg: tg, chatID: chatID}
}

func (n *WakeNotifier) SendWake(ctx context.Context, userID int64, nickname string, checks []wire.Check) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("wake notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	if user.TelegramThreadID == nil {
		slog.Debug("wake notifier: no thread, skipping", "user_id", userID, "nickname", nickname)
		return nil
	}
	now := time.Now()
	if last, ok, err := n.db.KV().GetMobileWakeNotifiedAt(userID); err != nil {
		slog.Warn("wake notifier: cooldown lookup failed", "user_id", userID, "err", err)
	} else if ok && now.Sub(last) < mobileWakeCooldown {
		slog.Debug("wake notifier: cooldown skip", "user_id", userID, "nickname", nickname)
		return nil
	}
	card := RenderWakeReport(nickname, checks)
	text := card.Render(CardOpts{MaxBytes: 3500})
	chatID := user.EffectiveTelegramChatID(n.chatID)
	if ktg, ok := n.tg.(lifecycleKeyboardTG); ok {
		_, err = ktg.SendMessageWithReplyKeyboard(ctx, chatID, user.TelegramThreadID, text, "", nil, mobileWakeKeyboard(userID))
	} else {
		_, err = n.tg.SendMessage(ctx, chatID, user.TelegramThreadID, text, "", nil)
	}
	if err != nil {
		slog.Warn("wake notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
	} else if err := n.db.KV().SetMobileWakeNotifiedAt(userID, now); err != nil {
		slog.Warn("wake notifier: cooldown persist failed", "user_id", userID, "err", err)
	}
	return err
}

// SleepNotifier sends a one-shot 🌙 info-card to the router's TG-topic when
// the heartbeat watcher detects MobileSleepAfter silence on a mobile-lifecycle
// user.
type SleepNotifier struct {
	db     *db.DB
	tg     LifecycleSendTG
	chatID int64
}

func NewSleepNotifier(d *db.DB, tg LifecycleSendTG, chatID int64) *SleepNotifier {
	return &SleepNotifier{db: d, tg: tg, chatID: chatID}
}

func (n *SleepNotifier) SendSleeping(ctx context.Context, userID int64, nickname string, lastSeen time.Time) error {
	user, err := n.db.Users().GetByID(userID)
	if err != nil || user == nil {
		slog.Warn("sleep notifier: user lookup", "user_id", userID, "err", err)
		return nil
	}
	if user.TelegramThreadID == nil {
		slog.Debug("sleep notifier: no thread, skipping", "user_id", userID, "nickname", nickname)
		return nil
	}
	card := RenderSleepInfo(nickname, lastSeen)
	text := card.Render(CardOpts{MaxBytes: 800})
	_, err = n.tg.SendMessage(ctx, user.EffectiveTelegramChatID(n.chatID), user.TelegramThreadID, text, "", nil)
	if err != nil {
		slog.Warn("sleep notifier: send failed", "user_id", userID, "nickname", nickname, "err", err)
	}
	return err
}

func mobileWakeKeyboard(userID int64) any {
	return struct {
		InlineKeyboard [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}{
		InlineKeyboard: [][]struct {
			Text         string `json:"text"`
			CallbackData string `json:"callback_data"`
		}{
			{
				{Text: "Диагностика", CallbackData: "diag_now:" + formatUserID(userID) + ":_menu"},
				{Text: "Повторить проверку", CallbackData: "force_recheck:" + formatUserID(userID) + ":_mobile"},
			},
			{
				{Text: "🛣 Маршруты", CallbackData: "panel:" + formatUserID(userID) + ":push:routes"},
				{Text: "HR-Neo проверка", CallbackData: "routes_hrneo_doctor:" + formatUserID(userID)},
			},
		},
	}
}

func formatUserID(id int64) string {
	return fmt.Sprintf("%d", id)
}
