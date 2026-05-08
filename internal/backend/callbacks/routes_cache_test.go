package callbacks

import (
	"testing"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

func TestRoutesCache_TTL(t *testing.T) {
	now := time.Date(2026, 5, 8, 14, 0, 0, 0, time.UTC)
	c := &RoutesCache{TTL: 30 * time.Second, Now: func() time.Time { return now }}
	snap := wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 3}}
	c.Put(42, snap)

	got, ok := c.Get(42)
	if !ok || got.Other.DNS != 3 {
		t.Fatalf("immediate get failed: %+v %v", got, ok)
	}

	now = now.Add(31 * time.Second)
	if _, ok := c.Get(42); ok {
		t.Errorf("expected miss after TTL")
	}
}

func TestRoutesCache_Invalidate(t *testing.T) {
	c := &RoutesCache{TTL: 30 * time.Second}
	c.Put(42, wire.RouteSnapshot{})
	c.Invalidate(42)
	if _, ok := c.Get(42); ok {
		t.Errorf("expected miss after invalidate")
	}
}

func TestRoutesCache_PerUser(t *testing.T) {
	c := &RoutesCache{TTL: 30 * time.Second}
	c.Put(1, wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 1}})
	c.Put(2, wire.RouteSnapshot{Other: wire.TunnelCounts{DNS: 2}})
	if got, _ := c.Get(1); got.Other.DNS != 1 {
		t.Errorf("user1: %+v", got)
	}
	if got, _ := c.Get(2); got.Other.DNS != 2 {
		t.Errorf("user2: %+v", got)
	}
}
