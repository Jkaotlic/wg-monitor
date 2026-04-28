package callbacks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func newTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// Users().Insert takes (nickname, rawToken, expectedExitIP, awgIface string)
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.1.1.1", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	return d, uid
}

func TestActionSilenceWritesUntil(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewSilenceAction(d)
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "silence", UserID: uid, CheckName: "awg_handshake", TTL: 4 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine, "⏸ Silenced") {
		t.Errorf("status line should start with '⏸ Silenced', got: %q", statusLine)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if st.SilencedUntil == nil {
		t.Fatal("SilencedUntil nil")
	}
	elapsed := time.Until(*st.SilencedUntil)
	if elapsed < 3*time.Hour+30*time.Minute || elapsed > 4*time.Hour+30*time.Minute {
		t.Errorf("expected silence ~4h, got %v", elapsed)
	}
}

func TestActionAckSetsAcked(t *testing.T) {
	d, uid := newTestDB(t)
	a := NewAckAction(d)
	statusLine, err := a.Apply(context.Background(), nil, Args{
		Action: "ack", UserID: uid, CheckName: "awg_handshake",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(statusLine, "✅ Ack'ed") {
		t.Errorf("status line should start with '✅ Ack'ed', got: %q", statusLine)
	}
	st, _ := d.State().Get(uid, "awg_handshake")
	if !st.Acked {
		t.Error("Acked not set to true")
	}
}
