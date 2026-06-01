package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// TestAdminEnsureTopics_BulkCreatesMissing verifies /ensure_topics walks
// every user and creates a topic only for those without one.
func TestAdminEnsureTopics_BulkCreatesMissing(t *testing.T) {
	d, _ := newTestDB(t) // creates "vasya"
	// vasya has no topic by default — make one user that DOES have one.
	betaID, err := d.Users().Insert("beta", "tok-beta", "2.2.2.2", "nwg1")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateThreadID(betaID, 8888); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 50, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)

	// Only vasya is missing a topic — exactly 1 CreateForumTopic call.
	if len(f.topicCalls) != 1 {
		t.Fatalf("want 1 CreateForumTopic call, got %d", len(f.topicCalls))
	}
	if f.topicCalls[0].Name != "👤 vasya" {
		t.Errorf("topic name: %q", f.topicCalls[0].Name)
	}
	// Reply summarises results.
	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 reply, got %d", len(f.sentMsgs))
	}
	reply := f.sentMsgs[0]
	for _, want := range []string{"✅ vasya", "1 создано", "1 пропущено"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply missing %q in:\n%s", want, reply)
		}
	}
}

// TestAdminRecreateTopic_RebuildsCurrentTopic: /recreate_topic inside a
// per-router topic creates a fresh thread_id and updates the DB.
func TestAdminRecreateTopic_RebuildsCurrentTopic(t *testing.T) {
	d, uid := newTestDB(t)
	const oldThread = int64(7777)
	if err := d.Users().UpdateThreadID(uid, oldThread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := oldThread
	msg := &tg.Message{
		MessageID: 51, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/recreate_topic",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.topicCalls) != 1 {
		t.Fatalf("want 1 CreateForumTopic call, got %d", len(f.topicCalls))
	}
	u, _ := d.Users().GetByNickname("vasya")
	if u.TelegramThreadID == nil || *u.TelegramThreadID == oldThread {
		t.Fatalf("thread id was not rebuilt: %v", u.TelegramThreadID)
	}
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "пересоздана") {
		t.Errorf("expected 'пересоздана' confirmation, got %v", f.sentMsgs)
	}
}

// TestAdminRecreateTopic_OutsideTopicReportsError: outside any per-router
// topic, /recreate_topic should refuse without touching DB or TG.
func TestAdminRecreateTopic_OutsideTopicReportsError(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 52, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/recreate_topic",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.topicCalls) != 0 {
		t.Fatalf("must NOT create topic outside per_router context, got %d", len(f.topicCalls))
	}
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "ВНУТРИ топика") {
		t.Errorf("expected helpful refusal, got %v", f.sentMsgs)
	}
}

// TestAdminThisIs_BindsCurrentTopicToNickname: /this_is vasya inside a
// topic associates the topic's thread_id with vasya's row.
func TestAdminThisIs_BindsCurrentTopicToNickname(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(12321)
	msg := &tg.Message{
		MessageID: 53, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/this_is vasya",
	}
	r.HandleMessage(context.Background(), msg)

	u, _ := d.Users().GetByID(uid)
	if u.TelegramThreadID == nil || *u.TelegramThreadID != tid {
		t.Fatalf("topic id not bound to vasya: %v", u.TelegramThreadID)
	}
	if len(f.topicCalls) != 0 {
		t.Errorf("/this_is must NOT call CreateForumTopic, got %d", len(f.topicCalls))
	}
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "привязан к роутеру vasya") {
		t.Errorf("expected bind confirmation, got %v", f.sentMsgs)
	}
}

// TestAdminThisIs_UnknownNicknameReportsError.
func TestAdminThisIs_UnknownNicknameReportsError(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := int64(99)
	msg := &tg.Message{
		MessageID: 54, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/this_is ghostuser",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], `"ghostuser" не найден`) {
		t.Errorf("expected 'not found' reply, got %v", f.sentMsgs)
	}
}

// TestAdminCommand_NonAdminIgnored: an admin slash command from a
// non-admin user must NOT trigger any side effects — the upstream
// admin-gate in HandleMessage rejects the message before handleAdminCommand.
func TestAdminCommand_NonAdminIgnored(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 55, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 99999},
		Text: "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)

	if len(f.topicCalls) != 0 || len(f.sentMsgs) != 0 {
		t.Errorf("non-admin must be ignored; calls=%d msgs=%d", len(f.topicCalls), len(f.sentMsgs))
	}
}

// TestAdminTopicHelp prints a cheat-sheet listing all four commands.
func TestAdminTopicHelp(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 56, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/topic_help",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	help := f.sentMsgs[0]
	for _, cmd := range []string{"/ensure_topics", "/recreate_topic", "/this_is", "/panel", "/topic_help"} {
		if !strings.Contains(help, cmd) {
			t.Errorf("help missing %q in:\n%s", cmd, help)
		}
	}
}

// TestAdminEnsureTopics_SendsWelcomeForFreshTopic: after /ensure_topics
// creates a topic, the freshly-created per_router topic gets a welcome
// message with the visible inline operator menu.
func TestAdminEnsureTopics_SendsWelcomeForFreshTopic(t *testing.T) {
	d, _ := newTestDB(t) // vasya has no topic by default
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 50, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)

	// Welcome lands in rkSends (SendMessageWithReplyKeyboard) — not sentMsgs.
	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Меню роутера vasya") {
			kb, ok := s.markup.(*tg.InlineKeyboardMarkup)
			if !ok || !keyboardContainsCallback(kb, "compat_btn:0:amnezia_premium") {
				t.Fatalf("welcome must carry visible operator menu, markup=%T %+v", s.markup, s.markup)
			}
			welcomeCount++
		}
	}
	if welcomeCount != 1 {
		t.Fatalf("want 1 welcome rkSend for vasya, got %d (all rkSends: %d)", welcomeCount, len(f.rkSends))
	}
}

// TestAdminRecreateTopic_SendsWelcomeAfterRebuild: after /recreate_topic,
// the new per_router topic gets a welcome message with the visible inline
// operator menu attached to the new thread.
func TestAdminRecreateTopic_SendsWelcomeAfterRebuild(t *testing.T) {
	d, uid := newTestDB(t)
	const oldThread = int64(7777)
	if err := d.Users().UpdateThreadID(uid, oldThread); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	tid := oldThread
	msg := &tg.Message{
		MessageID: 51, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		MessageThreadID: &tid, Text: "/recreate_topic",
	}
	r.HandleMessage(context.Background(), msg)

	var welcomeCount int
	for _, s := range f.rkSends {
		if strings.HasPrefix(s.text, "👋 Меню роутера vasya") {
			kb, ok := s.markup.(*tg.InlineKeyboardMarkup)
			if !ok || !keyboardContainsCallback(kb, "compat_btn:0:hidemyname") {
				t.Fatalf("welcome must carry visible operator menu, markup=%T %+v", s.markup, s.markup)
			}
			welcomeCount++
		}
	}
	if welcomeCount != 1 {
		t.Fatalf("want 1 welcome rkSend for vasya, got %d (rkSends: %d)", welcomeCount, len(f.rkSends))
	}
}

// TestAdminCommand_TolerateBotnameSuffix: TG appends @bot_name when the
// command is picked from the command menu in a group. Parsing must ignore
// the suffix.
func TestAdminCommand_TolerateBotnameSuffix(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 57, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 12345},
		Text: "/topic_help@wgmonitor_bot",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) != 1 {
		t.Fatalf("expected help reply despite @suffix, got %d", len(f.sentMsgs))
	}
}

// TestAdmin_EnsureTopics_FailsRendersHint: when the DB is closed before
// /ensure_topics runs, the error reply must contain both the ❌ badge and
// the 💡 hint block (Card shape, not a raw Go error string).
func TestAdmin_EnsureTopics_FailsRendersHint(t *testing.T) {
	d, _ := newTestDB(t)
	d.Close() // force DB error on next access
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})
	msg := &tg.Message{
		Chat: tg.Chat{ID: -100},
		From: tg.User{ID: 12345},
		Text: "/ensure_topics",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.sentMsgs) == 0 {
		t.Fatal("expected an error reply")
	}
	body := f.sentMsgs[0]
	if !strings.Contains(body, "❌") || !strings.Contains(body, "💡") {
		t.Errorf("error reply should contain ❌ badge AND 💡 hint, got: %s", body)
	}
}

// TestPanel_AdminOnlyGate verifies /panel from a non-admin user produces
// zero side effects — the HandleMessage admin-gate stops it before
// handleAdminCommand even runs.
func TestPanel_AdminOnlyGate(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTGFull{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 12345, MuteCutoffHour: 9})

	msg := &tg.Message{
		MessageID: 90, Chat: tg.Chat{ID: -100}, From: tg.User{ID: 99999},
		Text: "/panel",
	}
	r.HandleMessage(context.Background(), msg)
	if len(f.rkSends) != 0 || len(f.sentMsgs) != 0 {
		t.Errorf("non-admin /panel must be ignored; rkSends=%d sentMsgs=%d", len(f.rkSends), len(f.sentMsgs))
	}
}
