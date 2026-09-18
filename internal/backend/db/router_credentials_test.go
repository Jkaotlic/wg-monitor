package db

import (
	"testing"
	"time"
)

func TestRouterCredentials_PutGetDelete(t *testing.T) {
	d, id := newTestDBForRevive(t)
	repo := d.RouterCredentials()

	if _, _, _, ok, err := repo.Get(id); err != nil || ok {
		t.Fatalf("до сохранения: ok=%v err=%v", ok, err)
	}
	if err := repo.Put(id, []byte("nonce-1"), []byte("cipher-1"), reviveT0); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Повторное сохранение заменяет строку, а не копит вторую.
	later := reviveT0.Add(time.Hour)
	if err := repo.Put(id, []byte("nonce-2"), []byte("cipher-2"), later); err != nil {
		t.Fatalf("put 2: %v", err)
	}
	nonce, ct, at, ok, err := repo.Get(id)
	if err != nil || !ok || string(nonce) != "nonce-2" || string(ct) != "cipher-2" || !at.Equal(later) {
		t.Fatalf("get: %q %q %v %v %v", nonce, ct, at, ok, err)
	}
	saved, err := repo.SavedAt()
	if err != nil || len(saved) != 1 || !saved[id].Equal(later) {
		t.Fatalf("SavedAt: %v %v", saved, err)
	}

	gone, err := repo.Delete(id)
	if err != nil || !gone {
		t.Fatalf("delete: %v %v", gone, err)
	}
	gone, err = repo.Delete(id)
	if err != nil || gone {
		t.Fatalf("повторное delete: %v %v, want false", gone, err)
	}
	if _, _, _, ok, _ := repo.Get(id); ok {
		t.Fatal("после delete строка осталась")
	}
}

// Условное удаление: стираем только ту строку, которую видели. Если админ
// успел сохранить новый пароль, отказ входа по старому его не трогает.
func TestRouterCredentials_DeleteIfMatches(t *testing.T) {
	d, id := newTestDBForRevive(t)
	repo := d.RouterCredentials()
	if err := repo.Put(id, []byte("nonce-1"), []byte("cipher-1"), reviveT0); err != nil {
		t.Fatal(err)
	}
	if gone, err := repo.DeleteIfMatches(id, []byte("nonce-other")); err != nil || gone {
		t.Fatalf("чужой nonce: %v %v, want false", gone, err)
	}
	if gone, err := repo.DeleteIfMatches(id, []byte("nonce-1")); err != nil || !gone {
		t.Fatalf("свой nonce: %v %v, want true", gone, err)
	}
}

// Удаление роутера уносит его учётные данные каскадом.
func TestRouterCredentials_CascadeOnRouterDelete(t *testing.T) {
	d, id := newTestDBForRevive(t)
	if err := d.RouterCredentials().Put(id, []byte("n"), []byte("c"), reviveT0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL().Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := d.RouterCredentials().Get(id); err != nil || ok {
		t.Fatalf("после удаления роутера: ok=%v err=%v", ok, err)
	}
}
