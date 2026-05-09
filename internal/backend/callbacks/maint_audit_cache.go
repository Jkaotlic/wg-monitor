package callbacks

import (
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// simpleAuditCache is an in-memory per-user cache for the latest agent
// VersionAudit and FirmwareStatus payloads. Lost on backend restart — fine,
// next panel-open re-fetches via the cmd-queue.
//
// Used by:
//   - MaintNotifier (M11) — writes on incoming version_audit / firmware_status
//     CommandResults
//   - Router.handleMaintFwOpen (M10.2) — reads to render firmware screen
//     without a new fetch round-trip
//   - Router.dispatchSmartReply (M12) — reads to populate Updates section
//   - Router.openMaintPanelMessage (B2) — reads to render the panel from
//     cache instantly while a fresh audit refreshes in the background
type simpleAuditCache struct {
	mu  sync.RWMutex
	va  map[int64]wire.VersionAudit
	vat map[int64]time.Time // PutVersionAudit timestamp, for staleness checks
	fs  map[int64]wire.FirmwareStatus
}

func newSimpleAuditCache() *simpleAuditCache {
	return &simpleAuditCache{
		va:  make(map[int64]wire.VersionAudit),
		vat: make(map[int64]time.Time),
		fs:  make(map[int64]wire.FirmwareStatus),
	}
}

func (c *simpleAuditCache) PutVersionAudit(uid int64, va wire.VersionAudit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.va[uid] = va
	c.vat[uid] = time.Now()
}

func (c *simpleAuditCache) GetVersionAudit(uid int64) (wire.VersionAudit, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.va[uid]
	return v, ok
}

// GetVersionAuditWithAge returns the cached VersionAudit plus how long ago it
// was written. Returns (_, _, false) when nothing is cached for uid.
func (c *simpleAuditCache) GetVersionAuditWithAge(uid int64) (wire.VersionAudit, time.Duration, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.va[uid]
	if !ok {
		return v, 0, false
	}
	return v, time.Since(c.vat[uid]), true
}

func (c *simpleAuditCache) PutFirmwareStatus(uid int64, fs wire.FirmwareStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fs[uid] = fs
}

func (c *simpleAuditCache) GetFirmwareStatus(uid int64) (wire.FirmwareStatus, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.fs[uid]
	return v, ok
}
