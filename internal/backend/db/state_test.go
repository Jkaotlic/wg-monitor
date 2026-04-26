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

func TestStaleHardsCrossTimezone(t *testing.T) {
	d := newTestDB(t)
	tok := "6666666666666666666666666666666666666666666666666666666666666666"
	uid, _ := d.Users().Insert("zorya", tok, "2.2.2.2", "awg1")

	// Store incident with non-UTC local time (MSK UTC+3)
	msk := time.FixedZone("MSK", 3*3600)
	lastAlertAt := time.Now().In(msk).Add(-7 * time.Hour)
	st := IncidentState{
		CurrentStatus: "hard",
		HardSince:     ptrTime(lastAlertAt),
		LastAlertAt:   ptrTime(lastAlertAt),
	}
	if err := d.State().Save(uid, "awg_handshake", st); err != nil {
		t.Fatal(err)
	}

	// Cutoff in UTC — chronologically 1h more recent than lastAlertAt
	cutoff := time.Now().UTC().Add(-6 * time.Hour)
	stale, err := d.State().StaleHards(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 {
		t.Fatalf("cross-timezone: got %d stale hards, want 1", len(stale))
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
