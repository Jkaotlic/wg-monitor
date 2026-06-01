package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// fakeSink implements CommandEnqueuer for tests.
type fakeSink struct {
	enq []enqueued
}

type enqueued struct {
	UserID int64
	Cmd    wire.Command
}

func (f *fakeSink) Enqueue(uid int64, cmd wire.Command) error {
	f.enq = append(f.enq, enqueued{uid, cmd})
	return nil
}
func (f *fakeSink) EnqueueWithRef(uid int64, cmd wire.Command, _ cmdpkg.MessageRef) error {
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
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
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
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(-1 * time.Second)})
	if _, ok := store.consume(1, tok); ok {
		t.Error("expired token should be rejected")
	}
	// Side-effect: expired entry removed
	store.put(&pendingMaint{UserID: 1, Name: "x", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if _, ok := store.consume(1, tok); !ok {
		t.Error("re-issued token should work")
	}
}

func TestPendingMaintStore_WrongUserRejected(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	if _, ok := store.consume(2, tok); ok {
		t.Error("consume with wrong user should fail")
	}
}

func TestPendingMaintStore_WrongActorRejectedWithoutConsuming(t *testing.T) {
	store := newPendingMaintStore()
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, ActorTGID: 111, Name: "router", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})

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

func TestCooldownStore_BlocksWithinWindow(t *testing.T) {
	store := newCooldownStore()
	store.set(1, "router_reboot", 5*time.Minute)
	if rem := store.remaining(1, "router_reboot"); rem <= 0 {
		t.Errorf("expected positive remaining, got %v", rem)
	}
}

func TestCooldownStore_OtherUserNotBlocked(t *testing.T) {
	store := newCooldownStore()
	store.set(1, "router_reboot", 5*time.Minute)
	if rem := store.remaining(2, "router_reboot"); rem != 0 {
		t.Errorf("other user should not be in cooldown, got %v", rem)
	}
}

func TestCooldownStore_OtherActionNotBlocked(t *testing.T) {
	store := newCooldownStore()
	store.set(1, "router_reboot", 5*time.Minute)
	if rem := store.remaining(1, "firmware_install"); rem != 0 {
		t.Errorf("other action should not be in cooldown, got %v", rem)
	}
}

func TestCooldownStore_ExpiredCleared(t *testing.T) {
	store := newCooldownStore()
	store.set(1, "router_reboot", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if rem := store.remaining(1, "router_reboot"); rem != 0 {
		t.Errorf("expired cooldown should report 0, got %v", rem)
	}
}

func TestMaintConfirmAction_Hrneo(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "hrneo", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-1" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "hrneo", MaintToken: tok}
	status, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(status, "hrneo") {
		t.Errorf("status=%q should mention hrneo", status)
	}
	if len(sink.enq) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.enq))
	}
	if sink.enq[0].Cmd.Action != "service_restart" {
		t.Errorf("Action=%q want service_restart", sink.enq[0].Cmd.Action)
	}
	if sink.enq[0].Cmd.Args["name"] != "hrneo" {
		t.Errorf("Args=%v", sink.enq[0].Cmd.Args)
	}
	if cd.remaining(1, "router_reboot") != 0 {
		t.Error("hrneo should NOT set router_reboot cooldown")
	}
	if cd.remaining(1, "firmware_install") != 0 {
		t.Error("hrneo should NOT set firmware_install cooldown")
	}
}

func TestMaintConfirmAction_Awgmgr(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "awgmgr", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-2" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "awgmgr", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatal(err)
	}
	if sink.enq[0].Cmd.Args["name"] != "awgmgr" {
		t.Errorf("Args=%v want name=awgmgr", sink.enq[0].Cmd.Args)
	}
	if cd.remaining(1, "router_reboot") != 0 || cd.remaining(1, "firmware_install") != 0 {
		t.Error("awgmgr should NOT set any cooldown")
	}
}

func TestMaintConfirmAction_Router_AppliesCooldown(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "router", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-3" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "router", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatal(err)
	}
	if sink.enq[0].Cmd.Action != "service_restart" {
		t.Errorf("Action=%q", sink.enq[0].Cmd.Action)
	}
	if sink.enq[0].Cmd.Args["name"] != "router" {
		t.Errorf("Args=%v", sink.enq[0].Cmd.Args)
	}
	if cd.remaining(1, "router_reboot") <= 0 {
		t.Error("router should set router_reboot cooldown")
	}
}

func TestMaintConfirmAction_Firmware_AppliesCooldown(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "firmware", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-4" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "firmware", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatal(err)
	}
	if sink.enq[0].Cmd.Action != "firmware_install" {
		t.Errorf("Action=%q want firmware_install", sink.enq[0].Cmd.Action)
	}
	// firmware_install does not need a `name` arg
	if cd.remaining(1, "firmware_install") <= 0 {
		t.Error("firmware should set firmware_install cooldown")
	}
}

func TestMaintConfirmAction_OpkgUpgrade(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "opkg_upgrade", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-opkg" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "opkg_upgrade", MaintToken: tok}
	status, err := a.Apply(context.Background(), q, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "opkg_upgrade") {
		t.Fatalf("status should mention opkg_upgrade, got %q", status)
	}
	if len(sink.enq) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.enq))
	}
	if sink.enq[0].Cmd.Action != "opkg_upgrade" {
		t.Fatalf("Action=%q want opkg_upgrade", sink.enq[0].Cmd.Action)
	}
	if sink.enq[0].Cmd.Args != nil {
		t.Fatalf("opkg_upgrade should not need args, got %+v", sink.enq[0].Cmd.Args)
	}
}

func TestMaintConfirmAction_BadToken(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-x" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "hrneo", MaintToken: "deadbeef"}
	if _, err := a.Apply(context.Background(), q, args); err == nil {
		t.Error("expected error for unknown token")
	}
	if len(sink.enq) != 0 {
		t.Errorf("nothing should be enqueued on bad token, got %d entries", len(sink.enq))
	}
}

func TestMaintConfirmAction_UnknownName(t *testing.T) {
	store := newPendingMaintStore()
	cd := newCooldownStore()
	sink := &fakeSink{}
	tok := makeMaintToken()
	store.put(&pendingMaint{UserID: 1, Name: "wat", Token: tok, ExpiresAt: time.Now().Add(5 * time.Minute)})
	a := NewMaintConfirmAction(sink, store, cd, func() string { return "cmd-x" })
	q := &tg.CallbackQuery{From: tg.User{ID: 1}}
	args := Args{Action: "maint_confirm", UserID: 1, MaintName: "wat", MaintToken: tok}
	if _, err := a.Apply(context.Background(), q, args); err == nil {
		t.Error("expected error for unknown name")
	}
	if len(sink.enq) != 0 {
		t.Errorf("nothing should be enqueued on unknown name")
	}
}
