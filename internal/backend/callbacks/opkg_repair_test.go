package callbacks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestPendingOpkgRepair_PutConsume(t *testing.T) {
	s := newPendingOpkgRepairStore()
	p := &pendingOpkgRepair{
		UserID:    42,
		URL:       "https://x/Packages.gz",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.put(p)
	got, ok := s.consume(42, "tok1")
	if !ok {
		t.Fatal("consume should succeed")
	}
	if got.URL != "https://x/Packages.gz" {
		t.Errorf("URL=%q", got.URL)
	}
	if _, ok := s.consume(42, "tok1"); ok {
		t.Error("second consume should fail (single-use)")
	}
}

func TestPendingOpkgRepair_WrongUser(t *testing.T) {
	s := newPendingOpkgRepairStore()
	s.put(&pendingOpkgRepair{
		UserID: 42, URL: "u", Token: "t",
		ExpiresAt: time.Now().Add(time.Minute),
	})
	if _, ok := s.consume(99, "t"); ok {
		t.Error("wrong user must not consume")
	}
	// Token must remain for the rightful owner.
	if _, ok := s.consume(42, "t"); !ok {
		t.Error("owner must still be able to consume")
	}
}

func TestPendingOpkgRepair_Expired(t *testing.T) {
	s := newPendingOpkgRepairStore()
	s.put(&pendingOpkgRepair{
		UserID: 42, URL: "u", Token: "t",
		ExpiresAt: time.Now().Add(-time.Minute), // already expired
	})
	if _, ok := s.consume(42, "t"); ok {
		t.Error("expired must not consume")
	}
	// Expired entries get evicted on consume attempt.
	s.mu.Lock()
	_, present := s.m["t"]
	s.mu.Unlock()
	if present {
		t.Error("expired entry should have been evicted")
	}
}

func TestMakeOpkgRepairToken_Format(t *testing.T) {
	tok := makeOpkgRepairToken()
	if len(tok) != 8 {
		t.Errorf("token len=%d, want 8", len(tok))
	}
	for _, r := range tok {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, tok)
		}
	}
}

func TestOpkgRepairAction_EnqueueFailureKeepsToken(t *testing.T) {
	store := newPendingOpkgRepairStore()
	store.put(&pendingOpkgRepair{
		UserID: 42, URL: "https://repo.example/entware",
		Token: "tok1", ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	sink := &fakeSink{err: fmt.Errorf("queue down")}
	action := NewOpkgRepairAction(sink, store, func() string { return "cmd-opkg-disable" })
	q := &tg.CallbackQuery{Message: tg.Message{Chat: tg.Chat{ID: -100}, MessageID: 7}}
	args := Args{UserID: 42, OpkgRepairToken: "tok1"}

	if _, err := action.Apply(context.Background(), q, args); err == nil {
		t.Fatal("expected first enqueue to fail")
	}
	sink.err = nil
	if _, err := action.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("same token should work after transient enqueue failure: %v", err)
	}
	if len(sink.enq) != 1 || sink.enq[0].Cmd.Action != "opkg_feed_disable" {
		t.Fatalf("expected one opkg_feed_disable command, got %+v", sink.enq)
	}
}
