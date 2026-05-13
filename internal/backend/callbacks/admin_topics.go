package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// handleAdminCommand dispatches slash-style admin commands typed into any
// topic by the configured admin. Returns true if the message was handled
// (caller skips the normal ReplyKeyboard switch). HandleMessage's
// preceding admin gate (m.From.ID == cfg.AdminUserID) is the only auth.
//
// Recognized commands:
//
//	/ensure_topics                 — create forum topics for every router missing one
//	/recreate_topic                — rebuild the topic of the current per_router topic
//	/this_is <nickname>            — bind THIS topic's thread_id to <nickname>
//	/topic_help                    — print the admin cheat-sheet
func (r *Router) handleAdminCommand(ctx context.Context, m *tg.Message) bool {
	text := strings.TrimSpace(m.Text)
	if !strings.HasPrefix(text, "/") {
		return false
	}
	parts := strings.SplitN(text, " ", 2)
	cmd := parts[0]
	// Strip "@botname" suffix that TG appends when the user picks the
	// command from the command menu inside a group.
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	switch cmd {
	case "/ensure_topics":
		r.adminEnsureTopics(ctx, m)
		return true
	case "/recreate_topic":
		r.adminRecreateTopic(ctx, m)
		return true
	case "/this_is":
		r.adminThisIs(ctx, m, arg)
		return true
	case "/panel":
		r.adminPanelOpen(ctx, m)
		return true
	case "/topic_help":
		r.adminTopicHelp(ctx, m)
		return true
	}
	return false
}

func (r *Router) adminReply(ctx context.Context, m *tg.Message, text string) {
	if _, err := r.tg.SendMessage(ctx, m.Chat.ID, m.MessageThreadID, text, "", nil); err != nil {
		slog.Warn("admin command reply send failed", "err", err)
	}
}

// adminEnsureTopics walks every user; creates a forum topic for any
// without one and reports per-user status in one summary message. Same
// helper as Dispatcher.ensureTopic and CLI ensure-topics so behaviour
// (icon colour, title format, persistence step) stays identical.
func (r *Router) adminEnsureTopics(ctx context.Context, m *tg.Message) {
	users, err := r.d.Users().GetAll()
	if err != nil {
		sum, hint := alerts.HintFor("admin_ensure_topics", err.Error())
		card := alerts.Card{Badge: "❌", Label: "Не удалось получить пользователей", Summary: sum, Hint: hint}
		r.adminReply(ctx, m, card.Render(alerts.CardOpts{MaxBytes: 3500}))
		return
	}
	if len(users) == 0 {
		r.adminReply(ctx, m, "Пользователей нет.")
		return
	}
	var created, skipped, failed int
	var b strings.Builder
	b.WriteString("🪄 Создание недостающих тем\n\n")
	for _, u := range users {
		if u.TelegramThreadID != nil {
			skipped++
			continue
		}
		tid, err := alerts.EnsureTopicForUser(ctx, r.tg, r.d, r.cfg.ChatID, u.ID, false)
		if err != nil {
			fmt.Fprintf(&b, "❌ %s — %v\n", u.Nickname, err)
			failed++
			continue
		}
		// Welcome: best-effort, non-fatal. Skip if it fails — fresh topic
		// is still usable; the reply-kb will attach on the next bot message.
		if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, tid, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
			slog.Warn("welcome send failed (non-fatal)", "user", u.Nickname, "err", werr)
		}
		fmt.Fprintf(&b, "✅ %s — thread_id=%d\n", u.Nickname, tid)
		created++
	}
	fmt.Fprintf(&b, "\nИтого: %d создано, %d пропущено (уже есть), %d ошибок", created, skipped, failed)
	r.adminReply(ctx, m, b.String())
}

// adminRecreateTopic rebuilds the forum topic for the router that owns
// the message's MessageThreadID. The old topic stays in TG (history is
// preserved); only the DB binding is replaced. Reply goes into the OLD
// (originating) topic so the admin sees confirmation in place; future
// alerts will fire in the NEW topic.
func (r *Router) adminRecreateTopic(ctx context.Context, m *tg.Message) {
	if m.MessageThreadID == nil {
		r.adminReply(ctx, m, "/recreate_topic нужно писать ВНУТРИ топика конкретного роутера.")
		return
	}
	u, err := r.d.Users().GetByThreadID(*m.MessageThreadID)
	if err != nil {
		r.adminReply(ctx, m, "В этом топике не привязан ни один роутер. Сначала /this_is <nickname>.")
		return
	}
	oldID := int64(0)
	if u.TelegramThreadID != nil {
		oldID = *u.TelegramThreadID
	}
	tid, err := alerts.EnsureTopicForUser(ctx, r.tg, r.d, r.cfg.ChatID, u.ID, true)
	if err != nil {
		sum, hint := alerts.HintFor("admin_recreate_topic", err.Error())
		card := alerts.Card{Badge: "❌", Label: "Не удалось пересоздать топик", Summary: sum, Hint: hint}
		r.adminReply(ctx, m, card.Render(alerts.CardOpts{MaxBytes: 3500}))
		return
	}
	// Welcome: always fires on rebuild (new thread_id = new topic) so the
	// reply-keyboard attaches to the fresh thread. Non-fatal.
	if werr := alerts.SendWelcome(ctx, r.tg, r.cfg.ChatID, tid, u.Nickname, r.cfg.UI.KeyboardForTopic("per_router")); werr != nil {
		slog.Warn("welcome send failed (non-fatal)", "user", u.Nickname, "err", werr)
	}
	r.adminReply(ctx, m, fmt.Sprintf(
		"🔄 Тема для %s пересоздана.\n  Старая thread_id=%d (осталась в TG, можешь удалить руками)\n  Новая thread_id=%d — алерты пойдут туда.",
		u.Nickname, oldID, tid))
}

// adminThisIs binds the calling message's MessageThreadID to the router
// identified by <nickname>. Used when the operator has created the topic
// manually in TG and wants the bot to start using it (skipping
// createForumTopic).
func (r *Router) adminThisIs(ctx context.Context, m *tg.Message, arg string) {
	if arg == "" {
		r.adminReply(ctx, m, "Использование: /this_is <nickname>\n  (написать команду ВНУТРИ топика, который хочешь привязать)")
		return
	}
	if m.MessageThreadID == nil {
		r.adminReply(ctx, m, "/this_is нужно писать ВНУТРИ топика, который хочешь привязать. В общем чате это не имеет смысла.")
		return
	}
	nick := strings.TrimSpace(arg)
	u, err := r.d.Users().GetByNickname(nick)
	if err != nil {
		r.adminReply(ctx, m, fmt.Sprintf("Роутер %q не найден. Доступные — /list_users или CLI list-users.", nick))
		return
	}
	if err := r.d.Users().UpdateThreadID(u.ID, *m.MessageThreadID); err != nil {
		sum, hint := alerts.HintFor("admin_this_is", err.Error())
		card := alerts.Card{Badge: "❌", Label: "Не удалось сохранить привязку", Summary: sum, Hint: hint}
		r.adminReply(ctx, m, card.Render(alerts.CardOpts{MaxBytes: 3500}))
		return
	}
	r.adminReply(ctx, m, fmt.Sprintf("✅ Топик thread_id=%d привязан к роутеру %s. Алерты для %s теперь будут идти в этот топик.",
		*m.MessageThreadID, nick, nick))
}

func (r *Router) adminTopicHelp(ctx context.Context, m *tg.Message) {
	r.adminReply(ctx, m, `📚 Команды управления темами (только для админа):

/ensure_topics — создать темы для всех роутеров без темы (bulk).

/recreate_topic — пересоздать тему ТЕКУЩЕГО топика (пиши команду внутри топика роутера). Старая остаётся в TG, новая становится активной.

/this_is <nickname> — привязать ЭТОТ топик к роутеру <nickname>. Полезно если ты создал тему руками в TG и хочешь, чтобы алерты этого роутера шли в неё.

/panel — открыть админ-панель: оттуда можно отправить Maintenance / Routes / Status в любой роутер, или "оживить" все топики (добавить кнопки во все).

/topic_help — эта справка.

Подсказка: thread_id топика можно посмотреть в /list_users — он совпадает с message_thread_id из TG API.`)
}
