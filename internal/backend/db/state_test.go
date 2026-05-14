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

func TestStateAckedRoundTrip(t *testing.T) {
	d := newTestDB(t)
	tok := "7777777777777777777777777777777777777777777777777777777777777777"
	uid, _ := d.Users().Insert("u1", tok, "1.1.1.1", "nwg0")

	err := d.State().Save(uid, "awg_handshake", IncidentState{
		UserID:           uid,
		CheckName:        "awg_handshake",
		CurrentStatus:    "hard",
		ConsecutiveFails: 3,
		Acked:            true,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Acked {
		t.Errorf("Acked should round-trip true, got false")
	}
}

func TestStateStaleHardsFiltersAcked(t *testing.T) {
	d := newTestDB(t)
	tok := "8888888888888888888888888888888888888888888888888888888888888888"
	uid, _ := d.Users().Insert("u1", tok, "1.1.1.1", "nwg0")

	oldAlert := time.Now().Add(-7 * time.Hour)
	hardSince := oldAlert
	err := d.State().Save(uid, "awg_handshake", IncidentState{
		UserID:        uid,
		CheckName:     "awg_handshake",
		CurrentStatus: "hard",
		HardSince:     &hardSince,
		LastAlertAt:   &oldAlert,
		Acked:         true,
	})
	if err != nil {
		t.Fatal(err)
	}

	stale, err := d.State().StaleHards(time.Now().Add(-6 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Errorf("acked=1 row should not appear in StaleHards, got %d rows", len(stale))
	}
}

func TestBumpLastAlertAtNoOpsOnRecoveredIncident(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("u1", "h", "1.1.1.1", "nwg0")

	// Pre-state: status='ok' (recovered)
	origAlert := time.Now().Add(-1 * time.Hour)
	_ = d.State().Save(uid, "awg_handshake", IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "ok",
		LastAlertAt: &origAlert,
	})

	// Bump should be no-op since status != 'hard'
	newTime := time.Now()
	if err := d.State().BumpLastAlertAt(uid, "awg_handshake", newTime); err != nil {
		t.Fatal(err)
	}

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil {
		t.Fatal("LastAlertAt nil")
	}
	// Should be unchanged (within 1s of orig)
	if st.LastAlertAt.Sub(origAlert).Abs() > time.Second {
		t.Errorf("BumpLastAlertAt should not have updated recovered incident; got LastAlertAt=%v want ~%v", st.LastAlertAt, origAlert)
	}
}

func TestBumpLastAlertAtUpdatesHard(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("u2", "h2", "1.1.1.2", "nwg1")

	hardSince := time.Now().Add(-7 * time.Hour)
	origAlert := time.Now().Add(-7 * time.Hour)
	_ = d.State().Save(uid, "awg_handshake", IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &origAlert,
	})

	newTime := time.Now()
	if err := d.State().BumpLastAlertAt(uid, "awg_handshake", newTime); err != nil {
		t.Fatal(err)
	}

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil {
		t.Fatal("LastAlertAt nil")
	}
	if time.Since(*st.LastAlertAt) > time.Second {
		t.Errorf("expected LastAlertAt ~now, got %v ago", time.Since(*st.LastAlertAt))
	}
}

func TestSetLastAlert_UpdatesHardOnly(t *testing.T) {
	d := newTestDB(t)
	uid, _ := d.Users().Insert("u3", "h3", "1.1.1.3", "nwg2")

	hardSince := time.Now().Add(-3 * time.Hour)
	_ = d.State().Save(uid, "tunnel_awg11", IncidentState{
		UserID: uid, CheckName: "tunnel_awg11", CurrentStatus: "hard",
		HardSince: &hardSince,
	})

	now := time.Now()
	if err := d.State().SetLastAlert(uid, "tunnel_awg11", 42, now); err != nil {
		t.Fatal(err)
	}
	st, _ := d.State().Get(uid, "tunnel_awg11")
	if st.LastAlertMsgID == nil || *st.LastAlertMsgID != 42 {
		t.Errorf("LastAlertMsgID: %v want 42", st.LastAlertMsgID)
	}
	if st.LastAlertAt == nil || time.Since(*st.LastAlertAt) > time.Second {
		t.Errorf("LastAlertAt not bumped: %v", st.LastAlertAt)
	}
	// Recovered incident → no-op (mirrors BumpLastAlertAt safety).
	_ = d.State().Save(uid, "tunnel_awg11", IncidentState{
		UserID: uid, CheckName: "tunnel_awg11", CurrentStatus: "ok",
	})
	if err := d.State().SetLastAlert(uid, "tunnel_awg11", 99, time.Now()); err != nil {
		t.Fatal(err)
	}
	st2, _ := d.State().Get(uid, "tunnel_awg11")
	if st2.LastAlertMsgID != nil && *st2.LastAlertMsgID == 99 {
		t.Errorf("SetLastAlert should be no-op on recovered status, got msg_id=%d", *st2.LastAlertMsgID)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
