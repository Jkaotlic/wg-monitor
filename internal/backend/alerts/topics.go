package alerts

import (
	"context"
	"fmt"

	"github.com/anex/wg-monitor/internal/backend/db"
)

// TopicCreator is the slice of *tg.Client used by topic-creation paths.
// Extracted so the alerts package doesn't import the full tg client API,
// and so CLI/Router tests can drop a mock in without an HTTPS server.
type TopicCreator interface {
	CreateForumTopic(ctx context.Context, chatID int64, name string, iconColor int) (int64, error)
}

// TopicIconOrange matches the colour the Dispatcher uses on its lazy
// create path — keeps every topic in the chat visually consistent.
const TopicIconOrange = 0xFF8C00

// EnsureTopicForUser is the single source of truth for "make sure this
// user has a forum topic in TG, persist its id." Behaviour:
//
//   - force=false, user has TelegramThreadID  → no-op, return existing id
//   - force=false, user has none              → create + persist + return new id
//   - force=true                              → always create + persist + return new id
//     (the previous id, if any, is overwritten — the orphaned topic in
//     TG is left intact so historic messages aren't lost; operator can
//     delete it manually)
//
// NOT goroutine-safe. Callers that may invoke this concurrently for the
// same user (e.g. Dispatcher under alert pressure) must serialize
// themselves. CLI and Router admin commands call from single-threaded
// paths and need no extra lock.
func EnsureTopicForUser(ctx context.Context, tg TopicCreator, d *db.DB, chatID, userID int64, force bool) (int64, error) {
	u, err := d.Users().GetByID(userID)
	if err != nil {
		return 0, fmt.Errorf("ensure topic: %w", err)
	}
	if !force && u.TelegramThreadID != nil {
		return *u.TelegramThreadID, nil
	}
	tid, err := tg.CreateForumTopic(ctx, chatID, "👤 "+u.Nickname, TopicIconOrange)
	if err != nil {
		return 0, fmt.Errorf("ensure topic: createForumTopic for %s: %w", u.Nickname, err)
	}
	if err := d.Users().UpdateThreadID(userID, tid); err != nil {
		return 0, fmt.Errorf("ensure topic: persist thread id for %s: %w", u.Nickname, err)
	}
	return tid, nil
}

// WelcomeSender is the slice of *tg.Client used by SendWelcome. Narrow
// interface so tests can substitute a fake and the alerts package doesn't
// pull in the full TG client surface for one helper.
type WelcomeSender interface {
	SendMessageWithReplyKeyboard(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64, markup any) (int64, error)
}

// SendWelcome posts the first-contact message into a freshly-created
// per_router topic so operators immediately see the topic menu. Callers
// usually pass tg.OperatorMenuInlineKeyboardForTopic("per_router") so the
// controls stay visible even when Telegram drops the bottom reply keyboard.
func SendWelcome(ctx context.Context, tg WelcomeSender, chatID, threadID int64, nickname string, markup any) error {
	text := "👋 Меню роутера " + nickname + " готово.\n\n" +
		"Вот кнопки под этим сообщением — основные действия для топика. Ими можно пользоваться сразу, без slash-команд.\n" +
		"Начни с 📊 статуса или 🩺 проверки."
	t := threadID
	_, err := tg.SendMessageWithReplyKeyboard(ctx, chatID, &t, text, "", nil, markup)
	return err
}

// RepushKeyboard posts a fresh visible menu into an existing per_router
// topic. Used by /keyboard and admin recovery flows when Telegram clients
// hide or drop their local bottom keyboard state.
func RepushKeyboard(ctx context.Context, tg WelcomeSender, chatID, threadID int64, nickname string, markup any) error {
	text := "🪄 Меню роутера " + nickname + " обновлено.\n\n" +
		"Вот кнопки под этим сообщением снова доступны для работы в этом топике."
	t := threadID
	_, err := tg.SendMessageWithReplyKeyboard(ctx, chatID, &t, text, "", nil, markup)
	return err
}
