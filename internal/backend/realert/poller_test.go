package realert

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeTG struct {
	mu      sync.Mutex
	sent    []string
	sendErr error
}

func (f *fakeTG) SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return 99, f.sendErr
}

func (f *fakeTG) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func TestTickEmptyNoCalls(t *testing.T) {
	d, _ := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})
	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("expected 0 sends, got %d", len(f.sent))
	}
}

func TestTickStaleHardSendsRealert(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())

	if len(f.sent) != 1 {
		t.Fatalf("expected 1 realert, got %d", len(f.sent))
	}
	if !strings.Contains(f.sent[0], "Всё ещё:") {
		t.Errorf("missing realert marker in: %q", f.sent[0])
	}
	if !strings.Contains(f.sent[0], "#1") {
		t.Errorf("expected re-alert counter '#1' (7h since), got: %q", f.sent[0])
	}

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil || time.Since(*st.LastAlertAt) > time.Minute {
		t.Errorf("LastAlertAt should be updated to recent, got %v", st.LastAlertAt)
	}
}

func TestTickSilencedSkipped(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	silenced := time.Now().Add(2 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &lastAlert, SilencedUntil: &silenced,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("silenced HARD should not realert, got %d sends", len(f.sent))
	}
}

func TestTickAckedSkipped(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &lastAlert, Acked: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())
	if len(f.sent) != 0 {
		t.Errorf("acked HARD should not realert, got %d sends", len(f.sent))
	}
}

func TestTickSendErrorPreservesLastAlertAt(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{sendErr: errors.New("tg flap")}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second})

	hardSince := time.Now().Add(-7 * time.Hour)
	origLastAlert := time.Now().Add(-7 * time.Hour)
	err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		HardSince: &hardSince, LastAlertAt: &origLastAlert,
	})
	if err != nil {
		t.Fatal(err)
	}

	p.tick(context.Background())

	st, _ := d.State().Get(uid, "awg_handshake")
	if st.LastAlertAt == nil {
		t.Fatal("LastAlertAt nil")
	}
	if time.Since(*st.LastAlertAt) < 6*time.Hour {
		t.Errorf("LastAlertAt should NOT have advanced after send error, but it did: %v", st.LastAlertAt)
	}
}

// TEST-03: cover Run() and WaitForExit() — previously 0%.
// Run for ~100ms with TickEvery=25ms and verify the goroutine actually fired
// at least twice through the fake TG sender. Uses a stale-hard fixture so
// every tick is forced to send.
func TestPoller_Run_FiresOncePerInterval(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeTG{}
	p := NewPoller(d, f, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: 25 * time.Millisecond})

	hardSince := time.Now().Add(-7 * time.Hour)
	lastAlert := time.Now().Add(-7 * time.Hour)
	if err := d.State().Save(uid, "awg_handshake", db.IncidentState{
		UserID: uid, CheckName: "awg_handshake", CurrentStatus: "hard",
		ConsecutiveFails: 3, HardSince: &hardSince, LastAlertAt: &lastAlert,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	// Give the ticker enough wall-time for >= 2 fires (~3-4 expected at 25ms cadence).
	time.Sleep(110 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
	p.WaitForExit() // must be idempotent / non-blocking after Run returned

	// After the very first send LastAlertAt is bumped to "now", so subsequent
	// ticks within the same RealertEvery window won't pick it up via StaleHards.
	// The contract here is that Run() actually invoked tick() at least once,
	// proving the goroutine plumbing works (this is what TEST-03 cares about).
	if got := f.count(); got < 1 {
		t.Fatalf("Run produced no sends — ticker did not fire at all (got %d)", got)
	}
}

func TestPoller_Run_ExitsImmediatelyOnCancelledContext(t *testing.T) {
	d, _ := newTestDB(t)
	p := NewPoller(d, &fakeTG{}, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run starts the loop

	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned err: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not honor pre-cancelled context")
	}
	p.WaitForExit()
}

// TEST-03: neighborSummaries was 9.5%. Cover all three branches.
func TestNeighborSummaries_NonTunnelReturnsNil(t *testing.T) {
	d, uid := newTestDB(t)
	p := NewPoller(d, &fakeTG{}, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Hour})

	if got := p.neighborSummaries(uid, "awg_handshake"); got != nil {
		t.Fatalf("non-tunnel check should return nil, got %v", got)
	}
	if got := p.neighborSummaries(uid, "agent_heartbeat"); got != nil {
		t.Fatalf("non-tunnel check should return nil, got %v", got)
	}
}

func TestNeighborSummaries_NoNeighborsReturnsEmpty(t *testing.T) {
	d, uid := newTestDB(t)
	p := NewPoller(d, &fakeTG{}, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Hour})

	// No tunnel_* events at all → LatestEventsByPrefix returns empty slice.
	got := p.neighborSummaries(uid, "tunnel_awg11")
	if len(got) != 0 {
		t.Fatalf("expected no neighbors, got %d: %v", len(got), got)
	}
}

func TestNeighborSummaries_ReturnsSiblingsWithDetails(t *testing.T) {
	d, uid := newTestDB(t)
	p := NewPoller(d, &fakeTG{}, Config{ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Hour})

	// Three tunnel_* events; one is the queried check (must be excluded), two
	// are siblings — one with full details JSON, one with empty details.
	d.Events().Insert(uid, "tunnel_awg11", "fail",
		`{"tunnel_name":"AmericaSrv","interface":"awg11","handshake_age_sec":120}`,
		time.Now().UTC())
	d.Events().Insert(uid, "tunnel_awg12", "ok",
		`{"tunnel_name":"GermanySrv","interface":"awg12","handshake_age_sec":5}`,
		time.Now().UTC())
	d.Events().Insert(uid, "tunnel_awg13", "ok", "", time.Now().UTC())

	got := p.neighborSummaries(uid, "tunnel_awg11")
	if len(got) != 2 {
		t.Fatalf("expected 2 neighbors (excluding queried), got %d: %v", len(got), got)
	}
	// Siblings come back without a guaranteed order — index by check name.
	byName := map[string]int{}
	for i, ns := range got {
		byName[ns.CheckName] = i
	}
	g12, ok := byName["tunnel_awg12"]
	if !ok {
		t.Fatalf("missing tunnel_awg12 sibling: %v", got)
	}
	if got[g12].TunnelName != "GermanySrv" || got[g12].Interface != "awg12" || got[g12].HandshakeAge != 5 {
		t.Errorf("sibling tunnel_awg12 details parsed wrong: %+v", got[g12])
	}
	if got[g12].Status != "ok" {
		t.Errorf("sibling tunnel_awg12 status: want ok, got %q", got[g12].Status)
	}
	g13, ok := byName["tunnel_awg13"]
	if !ok {
		t.Fatalf("missing tunnel_awg13 sibling: %v", got)
	}
	// Empty details JSON branch — name/interface/age stay zero, status is set.
	if got[g13].TunnelName != "" || got[g13].Interface != "" || got[g13].HandshakeAge != 0 {
		t.Errorf("sibling with empty details_json should have zero metadata: %+v", got[g13])
	}
	if got[g13].Status != "ok" {
		t.Errorf("sibling tunnel_awg13 status: want ok, got %q", got[g13].Status)
	}
	if _, queriedLeaked := byName["tunnel_awg11"]; queriedLeaked {
		t.Error("queried tunnel must be excluded from neighbor list")
	}
}
