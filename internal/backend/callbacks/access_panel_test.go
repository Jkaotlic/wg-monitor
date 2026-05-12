package callbacks

import (
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
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

func TestAccessHomeMessage_TwoRouters(t *testing.T) {
	d, _ := newTestDB(t)
	uidA, _ := d.Users().Insert("alpha", "tok-a", "1.1.1.1", "awg11")
	uidB, _ := d.Users().Insert("beta", "tok-b", "2.2.2.2", "awg11")
	_ = d.Users().SetTelegramUserID(uidA, 100)
	_ = d.RouterOperators().Add(uidA, 200, 999)
	_ = d.RouterOperators().Add(uidA, 201, 999)
	// uidB: no owner, no operators

	text, kb := accessHomeMessage(d)
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "beta") {
		t.Errorf("text should list both routers: %q", text)
	}
	if !strings.Contains(text, "owner: 100") || !strings.Contains(text, "2 операт") {
		t.Errorf("alpha line should show owner + 2 operators: %q", text)
	}
	if !strings.Contains(text, "0 операт") {
		t.Errorf("beta line should show 0 operators: %q", text)
	}
	if len(kb.InlineKeyboard) < 3 {
		t.Errorf("expected ≥3 rows (2 routers + back), got %d", len(kb.InlineKeyboard))
	}
	_ = uidB
}

func TestAccessHomeMessage_NoRouters(t *testing.T) {
	// Open a fresh DB with no users seeded (don't use newTestDB which seeds "vasya").
	tmp := t.TempDir() + "/empty.db"
	emptyDB, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer emptyDB.Close()
	text, _ := accessHomeMessage(emptyDB)
	if !strings.Contains(text, "Роутеров нет") && !strings.Contains(text, "пуст") {
		t.Errorf("empty state text expected, got %q", text)
	}
}

func TestAccessRouterMessage(t *testing.T) {
	d, _ := newTestDB(t)
	uid, _ := d.Users().Insert("gamma", "tok-g", "3.3.3.3", "awg11")
	_ = d.Users().SetTelegramUserID(uid, 300)
	_ = d.RouterOperators().Add(uid, 301, 999)

	text, kb, err := accessRouterMessage(d, uid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(text, "gamma") {
		t.Errorf("text should name the router: %q", text)
	}
	if !strings.Contains(text, "300") {
		t.Errorf("text should show owner 300: %q", text)
	}
	if !strings.Contains(text, "301") {
		t.Errorf("text should show operator 301: %q", text)
	}
	// Each operator row contains a Remove button with the right callback.
	found := false
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.Contains(btn.CallbackData, "access:0:remove_op:") && strings.HasSuffix(btn.CallbackData, ":301") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected a remove_op button for op 301, got kb=%+v", kb)
	}
}

func TestAccessRouterMessage_NoOwner(t *testing.T) {
	d, _ := newTestDB(t)
	uid, _ := d.Users().Insert("delta", "tok-d", "4.4.4.4", "awg11")
	text, _, err := accessRouterMessage(d, uid)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(text, "не привязан") && !strings.Contains(text, "TOFU") {
		t.Errorf("unbound-owner label expected, got %q", text)
	}
}

func TestAccessRouterMessage_UnknownRouter(t *testing.T) {
	d, _ := newTestDB(t)
	_, _, err := accessRouterMessage(d, 9999)
	if err == nil {
		t.Error("expected error for unknown router id")
	}
}
