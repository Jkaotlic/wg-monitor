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
// per_router topic so the reply-keyboard buttons attach immediately (TG
// only re-installs the persistent keyboard on bot-originated messages —
// an empty topic has none until something arrives).
//
// markup must be the value returned by callbacks/UIConfigSnapshot.KeyboardForTopic("per_router")
// so the same compat-inline / reply-kb switch the rest of the app uses
// is honoured here. Pass nil to skip the keyboard entirely (not recommended).
func SendWelcome(ctx context.Context, tg WelcomeSender, chatID, threadID int64, nickname string, markup any) error {
	text := "👋 Топик роутера " + nickname + " готов.\n\n" +
		"Кнопки внизу — то, что я умею. Тапни 📊 чтобы посмотреть статус прямо сейчас."
	t := threadID
	_, err := tg.SendMessageWithReplyKeyboard(ctx, chatID, &t, text, "", nil, markup)
	return err
}

// RepushKeyboard re-attaches the reply-keyboard to an existing per_router
// topic by sending a short confirmation message that carries the markup.
// Used by the /keyboard slash command: TG can drop a topic's persistent
// keyboard if the operator deletes the bot's last message in the topic,
// and this is the explicit recovery path.
func RepushKeyboard(ctx context.Context, tg WelcomeSender, chatID, threadID int64, nickname string, markup any) error {
	text := "🪄 Кнопки восстановлены — " + nickname + ".\n" +
		"Если они опять пропадут, тапни /keyboard внутри этого топика."
	t := threadID
	_, err := tg.SendMessageWithReplyKeyboard(ctx, chatID, &t, text, "", nil, markup)
	return err
}
