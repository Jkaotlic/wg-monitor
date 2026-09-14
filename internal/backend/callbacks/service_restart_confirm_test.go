package callbacks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// fakeSink implements CommandEnqueuer for tests.
type fakeSink struct {
	enq []enqueued
	err error
}

type enqueued struct {
	UserID int64
	Cmd    wire.Command
}

func (f *fakeSink) Enqueue(uid int64, cmd wire.Command) error {
	if f.err != nil {
		return f.err
	}
	f.enq = append(f.enq, enqueued{uid, cmd})
	return nil
}
func (f *fakeSink) EnqueueWithRef(uid int64, cmd wire.Command, _ cmdpkg.MessageRef) error {
	if f.err != nil {
		return f.err
	}
	f.enq = append(f.enq, enqueued{uid, cmd})
	return nil
}

func TestPendingMaintStore_HappyPath(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "hrneo", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	got, ok := store.consume(1, tok)
	if !ok {
		t.Fatal("consume should succeed")
	}
	if got.Name != "hrneo" {
		t.Errorf("Name=%q want hrneo", got.Name)
	}
}

func TestPendingMaintStore_ReplayRejected(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if _, ok := store.consume(1, tok); !ok {
		t.Fatal("first consume should succeed")
	}
	if _, ok := store.consume(1, tok); ok {
		t.Error("replay should be rejected")
	}
}

func TestPendingMaintStore_ExpiredRejected(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(-1 * time.Second)})
	if _, ok := store.consume(1, tok); ok {
		t.Error("expired token should be rejected")
	}
	store.put(&pendingMaint{UserID: 1, Name: "x", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if _, ok := store.consume(1, tok); !ok {
		t.Error("re-issued token should work")
	}
}

func TestPendingMaintStore_WrongUserRejected(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if _, ok := store.consume(2, tok); ok {
		t.Error("consume with wrong user should fail")
	}
}

func TestPendingMaintStore_WrongActorRejectedWithoutConsuming(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, ActorTGID: 111, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})

	if _, ok := store.consumeForActor(1, 222, tok); ok {
		t.Fatal("consume with wrong actor should fail")
	}
	got, ok := store.consumeForActor(1, 111, tok)
	if !ok {
		t.Fatal("right actor should still be able to consume after wrong actor tap")
	}
	if got.ActorTGID != 111 {
		t.Fatalf("ActorTGID=%d want 111", got.ActorTGID)
	}
}

func TestPendingMaintStore_UnknownTokenRejected(t *testing.T) {
	store := newPendingMaintStore()
	if _, ok := store.consume(1, "deadbeef"); ok {
		t.Error("consume of unknown token should fail")
	}
}

func TestMakeMaintToken_HexAndUnique(t *testing.T) {
	a := makeMaintToken()
	b := makeMaintToken()
	if len(a) != 8 {
		t.Errorf("token length=%d want 8", len(a))
	}
	for _, r := range a {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex character %q in token %q", r, a)
		}
	}
	if a == b {
		t.Errorf("two consecutive tokens collided: %q == %q", a, b)
	}
}

func TestMaintConfirmAction_RestartsBotServices(t *testing.T) {
	// Тост -- человеку: человеческое имя службы (HydraRoute Neo / awg-manager),
	// а не внутренний токен (hrneo_stop и т.п.), см. tg.NameToDisplay.
	wantDisplay := map[string]string{
		"hrneo":       "HydraRoute Neo",
		"hrneo_start": "HydraRoute Neo",
		"hrneo_stop":  "HydraRoute Neo",
		"awgmgr":      "awg-manager",
	}
	for _, name := range []string{"hrneo", "hrneo_start", "hrneo_stop", "awgmgr"} {
		store := newPendingMaintStore()
		sink := &fakeSink{}
		tok := makeMaintToken()
		store.put(&pendingMaint{UserID: 1, Name: name, Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
		a := NewMaintConfirmAction(sink, store, func() string { return "cmd-1" })
		status, err := a.Apply(context.Background(), &tg.CallbackQuery{From: tg.User{ID: 1}}, Args{Action: "maint_confirm", UserID: 1, MaintName: name, MaintToken: tok})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(status, wantDisplay[name]) {
			t.Errorf("%s: status=%q, want to contain %q", name, status, wantDisplay[name])
		}
		if strings.Contains(status, name) && name != wantDisplay[name] {
			t.Errorf("%s: toast leaked internal token: status=%q", name, status)
		}
		if len(sink.enq) != 1 || sink.enq[0].Cmd.Action != "service_restart" || sink.enq[0].Cmd.Args["name"] != name {
			t.Errorf("%s: enq=%+v", name, sink.enq)
		}
	}
}

// Перезагрузка роутера и прошивка переехали в мини-апп: даже живой токен из
// старого сообщения не ставит их в очередь.
func TestMaintConfirmAction_RefusesMovedActions(t *testing.T) {
	for _, name := range []string{"router", "firmware", "opkg_upgrade", "wat"} {
		store := newPendingMaintStore()
		sink := &fakeSink{}
		tok := makeMaintToken()
		store.put(&pendingMaint{UserID: 1, Name: name, Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
		a := NewMaintConfirmAction(sink, store, func() string { return "cmd-x" })
		if _, err := a.Apply(context.Background(), &tg.CallbackQuery{From: tg.User{ID: 1}}, Args{Action: "maint_confirm", UserID: 1, MaintName: name, MaintToken: tok}); err == nil {
			t.Errorf("%s: ожидался отказ", name)
		}
		if len(sink.enq) != 0 {
			t.Errorf("%s: ушло в очередь %+v", name, sink.enq)
		}
	}
}

func TestMaintConfirmAction_EnqueueFailureKeepsToken(t *testing.T) {
	store := newPendingMaintStore()
	sink := &fakeSink{err: fmt.Errorf("queue down")}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, ActorTGID: 111, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, func() string { return "cmd-retry" })
	q := &tg.CallbackQuery{From: tg.User{ID: 111}, Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "awgmgr", MaintToken: tok}

	if _, err := a.Apply(context.Background(), q, args); err == nil {
		t.Fatal("expected first enqueue to fail")
	}
	sink.err = nil
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("same token should work after transient enqueue failure: %v", err)
	}
	if len(sink.enq) != 1 || sink.enq[0].Cmd.Action != "service_restart" {
		t.Fatalf("expected one queued awg-manager restart, got %+v", sink.enq)
	}
}

func TestMaintConfirmAction_BadToken(t *testing.T) {
	store := newPendingMaintStore()
	sink := &fakeSink{}
	a := NewMaintConfirmAction(sink, store, func() string { return "cmd-x" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "hrneo", MaintToken: "deadbeef"}
	if _, err := a.Apply(context.Background(), q, args); err == nil {
		t.Error("expected error for unknown token")
	}
	if len(sink.enq) != 0 {
		t.Errorf("nothing should be enqueued on bad token, got %d entries", len(sink.enq))
	}
}
