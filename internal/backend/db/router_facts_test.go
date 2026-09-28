package db

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestDBForFacts(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return d, id
}

// Блок заменяется целиком: удалённый VPN-туннель не должен пережить новый отчёт.
func TestRouterFactsUpsertReplacesBlock(t *testing.T) {
	d, uid := newTestDBForFacts(t)
	at := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	if err := d.RouterFacts().Upsert(uid, FactExit, []byte(`{"tunnels":{"awg10":{},"awg11":{}}}`), at, at); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Minute)
	if err := d.RouterFacts().Upsert(uid, FactExit, []byte(`{"tunnels":{"awg11":{}}}`), later, later); err != nil {
		t.Fatal(err)
	}
	all, err := d.RouterFacts().All(uid)
	if err != nil {
		t.Fatal(err)
	}
	got := all[FactExit]
	if string(got.Body) != `{"tunnels":{"awg11":{}}}` {
		t.Fatalf("body = %s", got.Body)
	}
	if !got.At.Equal(later) || !got.ReceivedAt.Equal(later) {
		t.Fatalf("время: at=%v received=%v", got.At, got.ReceivedAt)
	}
	if len(all) != 1 {
		t.Fatalf("видов %d, хотим 1", len(all))
	}
}

func TestRouterFactsEmptyForUnknownRouter(t *testing.T) {
	d, _ := newTestDBForFacts(t)
	all, err := d.RouterFacts().All(424242)
	if err != nil || len(all) != 0 {
		t.Fatalf("all=%v err=%v", all, err)
	}
}
