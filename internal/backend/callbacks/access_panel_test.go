package callbacks

import (
	"testing"
	"time"
)

func TestPendingAddOperator_PutGetClear(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, 5*time.Minute)
	got, ok := s.get(42)
	if !ok {
		t.Fatal("get should succeed")
	}
	if got.RouterID != 100 {
		t.Errorf("RouterID=%d", got.RouterID)
	}
	s.clear(42)
	if _, ok := s.get(42); ok {
		t.Error("after clear, get should fail")
	}
}

func TestPendingAddOperator_Expired(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, -time.Minute) // already expired
	if _, ok := s.get(42); ok {
		t.Error("expired entry must not be returned")
	}
	// Expired entries get evicted on get attempt.
	s.mu.Lock()
	_, present := s.m[42]
	s.mu.Unlock()
	if present {
		t.Error("expired entry should have been evicted")
	}
}

func TestPendingAddOperator_PutReplacesOld(t *testing.T) {
	s := newPendingAddOperatorStore()
	s.put(42, 100, 5*time.Minute)
	s.put(42, 200, 5*time.Minute) // same admin, different router
	got, ok := s.get(42)
	if !ok {
		t.Fatal("get should succeed")
	}
	if got.RouterID != 200 {
		t.Errorf("RouterID=%d, want 200 (replacement)", got.RouterID)
	}
}
