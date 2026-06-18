package db

import (
	"context"
	"testing"
)

// TestHealthCheck_OKOnLiveDB proves a healthy DB passes the readyz check.
func TestHealthCheck_OKOnLiveDB(t *testing.T) {
	d := newTestDB(t)
	if err := d.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck on a fresh DB should succeed, got: %v", err)
	}
}

// TestHealthCheck_FailsOnBrokenDB proves the check actually exercises the read
// path — once the DB is closed, a read fails (a bare connection ping might not),
// so /readyz turns 503 instead of falsely reporting healthy.
func TestHealthCheck_FailsOnBrokenDB(t *testing.T) {
	d := newTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := d.HealthCheck(context.Background()); err == nil {
		t.Fatal("HealthCheck on a closed DB must return an error")
	}
}
