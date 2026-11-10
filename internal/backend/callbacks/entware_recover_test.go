package callbacks

import (
	"context"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"testing"
)

type recoveryFake struct{ calls int }

func (f *recoveryFake) Start(int64) (string, error)        { f.calls++; return "fixture-job", nil }
func (f *recoveryFake) Latest(int64) (provision.Job, bool) { return provision.Job{}, false }
func TestEntwareRecoverRequiresAdminDMConfirmation(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeRouterTG{}
	ops := &recoveryFake{}
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.SetEntwareRecovery(ops)
	r.HandleMessage(context.Background(), dm(100, "/entware_recover vasya"))
	r.HandleMessage(context.Background(), &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/entware_recover vasya"})
	if ops.calls != 0 || len(f.rkSends) != 0 {
		t.Fatal("nonadmin/group request accepted")
	}
	r.HandleMessage(context.Background(), dm(42, "/entware_recover vasya"))
	if ops.calls != 0 || len(f.rkSends) != 1 {
		t.Fatal("ran before confirmation")
	}
	kb := f.rkSends[0].markup.(*tg.InlineKeyboardMarkup)
	data := kb.InlineKeyboard[0][0].CallbackData
	q := &tg.CallbackQuery{ID: "x", Data: data, From: tg.User{ID: 42}, Message: tg.Message{Chat: tg.Chat{ID: 42}}}
	r.HandleCallback(context.Background(), q)
	r.HandleCallback(context.Background(), q)
	if ops.calls != 1 {
		t.Fatalf("confirmation used %d times", ops.calls)
	}
}
