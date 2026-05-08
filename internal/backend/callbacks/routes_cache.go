package callbacks

import (
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// RoutesCache stores the last-known RouteSnapshot per user with a TTL.
// Backend invalidates the entry after a successful route_rebind so that
// "К маршрутам" after a rebind shows fresh data.
type RoutesCache struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	entries map[int64]routesCacheEntry
}

type routesCacheEntry struct {
	snap wire.RouteSnapshot
	at   time.Time
}

func (c *RoutesCache) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *RoutesCache) Get(userID int64) (wire.RouteSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[userID]
	if !ok {
		return wire.RouteSnapshot{}, false
	}
	if c.now().Sub(e.at) > c.TTL {
		return wire.RouteSnapshot{}, false
	}
	return e.snap, true
}

func (c *RoutesCache) Put(userID int64, snap wire.RouteSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[int64]routesCacheEntry)
	}
	c.entries[userID] = routesCacheEntry{snap: snap, at: c.now()}
}

func (c *RoutesCache) Invalidate(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, userID)
}
