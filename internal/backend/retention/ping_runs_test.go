package retention

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestPruneRemovesOldPingRuns(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, _ := d.Users().Insert("r", "tok-r", "198.51.100.1", "awg0")
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	_ = d.PingRuns().Upsert(uid, db.PingRunRow{TunnelID: "awg11", From: now.AddDate(0, 0, -40), To: now.AddDate(0, 0, -40), Fails: 1})
	_ = d.PingRuns().Upsert(uid, db.PingRunRow{TunnelID: "awg11", From: now.Add(-time.Hour), To: now.Add(-time.Hour), Fails: 1})
	p := &Policy{DB: d, Cfg: Config{EventsDays: 30}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	if err := p.prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := d.PingRuns().Since(uid, now.AddDate(0, 0, -60))
	if len(rows) != 1 {
		t.Fatalf("после чистки серий %d, хотим 1", len(rows))
	}
}
