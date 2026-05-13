package callbacks

import (
	"testing"
	"time"
)

func TestDiagCache_PutGet(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("raw json body", 5*time.Minute)
	if tok == "" {
		t.Fatal("Put returned empty token")
	}
	got, ok := c.Get(tok)
	if !ok || got != "raw json body" {
		t.Errorf("Get(%q) = (%q, %v), want (\"raw json body\", true)", tok, got, ok)
	}
}

func TestDiagCache_TokenIsHex8(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("x", time.Minute)
	if len(tok) != 8 {
		t.Errorf("token len=%d, want 8", len(tok))
	}
	for _, r := range tok {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in token %q", r, tok)
		}
	}
}

func TestDiagCache_ExpiresAfterTTL(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("x", 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, ok := c.Get(tok); ok {
		t.Error("expected Get to fail after TTL elapsed")
	}
}

func TestDiagCache_MultiGetReturnsBodyEachTime(t *testing.T) {
	c := newDiagCache()
	tok := c.Put("body", time.Minute)
	for i := 0; i < 3; i++ {
		if got, ok := c.Get(tok); !ok || got != "body" {
			t.Errorf("call %d: got=(%q,%v), want (\"body\",true)", i, got, ok)
		}
	}
}
