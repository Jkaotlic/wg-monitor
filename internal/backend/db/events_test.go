package db

import (
	"testing"
	"time"
)

func TestEventsInsertAndLatest(t *testing.T) {
	d := newTestDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	now := time.Now().UTC()
	if err := d.Events().Insert(uid, "awg_handshake", "ok", `{"x":1}`, now.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "awg_handshake", "fail", `{"x":2}`, now); err != nil {
		t.Fatal(err)
	}

	got, err := d.Events().LatestPerUser(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.IsZero() {
		t.Fatal("got zero timestamp")
	}
	if got.Before(now.Add(-time.Second)) {
		t.Fatalf("got %v want close to %v", got, now)
	}
}

func TestEventsPruneBefore(t *testing.T) {
	d := newTestDB(t)
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	old := time.Now().Add(-30 * 24 * time.Hour)
	fresh := time.Now()
	d.Events().Insert(uid, "x", "ok", "", old)
	d.Events().Insert(uid, "x", "ok", "", fresh)

	n, err := d.Events().PruneBefore(time.Now().Add(-7 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
}
