package callbacks

import (
	"sync"

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
type simpleAuditCache struct {
	mu sync.RWMutex
	va map[int64]wire.VersionAudit
	fs map[int64]wire.FirmwareStatus
}

func newSimpleAuditCache() *simpleAuditCache {
	return &simpleAuditCache{
		va: make(map[int64]wire.VersionAudit),
		fs: make(map[int64]wire.FirmwareStatus),
	}
}

func (c *simpleAuditCache) PutVersionAudit(uid int64, va wire.VersionAudit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.va[uid] = va
}

func (c *simpleAuditCache) GetVersionAudit(uid int64) (wire.VersionAudit, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.va[uid]
	return v, ok
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
