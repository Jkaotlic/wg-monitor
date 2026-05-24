package callbacks

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func TestHelp_AdminGetsFullBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	for _, want := range []string{"Алерты", "Кнопки в топике", "Админ-команды", "/panel", "Amnezia Premium", "HideMy.name", ".conf"} {
		if !strings.Contains(body, want) {
			t.Errorf("admin /help missing %q in body:\n%s", want, body)
		}
	}
}

func TestHelp_OperatorGetsOperatorBody(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateThreadID(uid, 55); err != nil {
		t.Fatal(err)
	}
	_ = d.Users().SetTelegramUserID(uid, 100)
	_ = d.RouterOperators().Add(uid, 200, 42)

	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	tid := int64(55)
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 200}, MessageThreadID: &tid, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	if len(f.sentMsgs) != 1 {
		t.Fatalf("want 1 help reply, got %d", len(f.sentMsgs))
	}
	body := f.sentMsgs[0]
	if strings.Contains(body, "/panel —") || strings.Contains(body, "Админ-команды") {
		t.Errorf("operator help must NOT include admin section:\n%s", body)
	}
	for _, want := range []string{"Кнопки в топике", "очередь", "Amnezia Premium", "HideMy.name"} {
		if !strings.Contains(body, want) {
			t.Errorf("operator help missing %q:\n%s", want, body)
		}
	}
}

func TestHelp_StrangerDoesNotSeeAdminBody(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	r := NewRouter(d, f, Config{ChatID: -100, AdminUserID: 42})
	msg := &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 999}, Text: "/help"}
	r.HandleMessage(context.Background(), msg)

	for _, m := range f.sentMsgs {
		if strings.Contains(m, "Админ-команды") {
			t.Errorf("stranger must not see admin body, got: %s", m)
		}
	}
}
