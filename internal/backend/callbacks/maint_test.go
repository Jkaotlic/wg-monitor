package callbacks

import (
	"testing"
	"time"
)

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
