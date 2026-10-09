package db

import (
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestUnstickEvents_InsertIdempotentAndPrune(t *testing.T) {
	d, uid := newTestDBForFacts(t)
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ev := wire.UnstickEvent{ID: "1-nwg0", TunnelID: "nwg0", From: "broken", Steps: []string{"restart"}, Result: wire.UnstickFixed, To: "running", At: at}
	for i := 0; i < 2; i++ {
		res, err := d.SQL().Exec(InsertUnstickEventSQL, UnstickEventArgs(uid, ev)...)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := res.RowsAffected()
		if want := int64(1 - i); n != want {
			t.Fatalf("insert #%d rows=%d", i, n)
		}
	}
	if c, err := d.UnstickEvents().Count(uid); err != nil || c != 1 {
		t.Fatalf("count: %d %v", c, err)
	}
	if n, err := d.UnstickEvents().PruneBefore(at.Add(time.Second)); err != nil || n != 1 {
		t.Fatalf("prune: %d %v", n, err)
	}
}
