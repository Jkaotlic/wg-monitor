package callbacks

import (
	"testing"
	"time"
)

func TestPendingConfirmStoreRejectsWrongActorWithoutConsuming(t *testing.T) {
	threadID := int64(77)
	store := newPendingConfirmStore()
	store.put(&pendingConfirm{
		UserID:    1,
		ActorTGID: 111,
		ThreadID:  &threadID,
		Action:    "amz_revoke_confirm",
		Target:    "key:de",
		Token:     "tok1",
		ExpiresAt: time.Now().Add(time.Minute),
	})

	if _, ok := store.consume(1, 222, &threadID, "amz_revoke_confirm", "key:de", "tok1"); ok {
		t.Fatal("wrong actor must not consume confirm token")
	}
	if _, ok := store.consume(1, 111, &threadID, "amz_revoke_confirm", "key:de", "tok1"); !ok {
		t.Fatal("original actor should still consume token after wrong actor tap")
	}
	if _, ok := store.consume(1, 111, &threadID, "amz_revoke_confirm", "key:de", "tok1"); ok {
		t.Fatal("confirm token must be single-use")
	}
}

func TestPendingConfirmStoreRejectsExpiredToken(t *testing.T) {
	store := newPendingConfirmStore()
	store.put(&pendingConfirm{
		UserID:    1,
		ActorTGID: 111,
		Action:    "hmn_delete_confirm",
		Target:    "code1",
		Token:     "tok2",
		ExpiresAt: time.Now().Add(-time.Second),
	})

	if _, ok := store.consume(1, 111, nil, "hmn_delete_confirm", "code1", "tok2"); ok {
		t.Fatal("expired confirm token must not be accepted")
	}
}

