package db

import (
	"testing"
	"time"
)

func TestIncidentStateRoundtrip(t *testing.T) {
	d := newTestDB(t)
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	got, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStatus != "ok" || got.ConsecutiveFails != 0 {
		t.Fatalf("default state: %+v", got)
	}

	got.ConsecutiveFails = 2
	got.CurrentStatus = "fail"
	if err := d.State().Save(uid, "awg_handshake", got); err != nil {
		t.Fatal(err)
	}

	again, _ := d.State().Get(uid, "awg_handshake")
	if again.ConsecutiveFails != 2 || again.CurrentStatus != "fail" {
		t.Fatalf("roundtrip: %+v", again)
	}
}

func TestDailySoftFlapsIncr(t *testing.T) {
	d := newTestDB(t)
	tok := "4444444444444444444444444444444444444444444444444444444444444444"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	today := time.Now().UTC().Format("2006-01-02")

	for i := 0; i < 3; i++ {
		if err := d.State().IncSoftFlap(uid, "dns_doh", today); err != nil {
			t.Fatal(err)
		}
	}
	n, err := d.State().GetSoftFlap(uid, "dns_doh", today)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("got %d", n)
	}
}

func TestStaleHardsForRealert(t *testing.T) {
	d := newTestDB(t)
	tok := "5555555555555555555555555555555555555555555555555555555555555555"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	st := IncidentState{
		CurrentStatus: "hard",
		HardSince:     ptrTime(time.Now().Add(-7 * time.Hour)),
		LastAlertAt:   ptrTime(time.Now().Add(-7 * time.Hour)),
	}
	if err := d.State().Save(uid, "awg_handshake", st); err != nil {
		t.Fatal(err)
	}

	stale, err := d.State().StaleHards(time.Now().Add(-6 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 {
		t.Fatalf("got %d stale hards: %+v", len(stale), stale)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
