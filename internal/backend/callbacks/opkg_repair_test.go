package callbacks

import (
	"testing"
	"time"
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
