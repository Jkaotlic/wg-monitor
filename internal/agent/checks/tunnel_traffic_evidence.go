package checks

import (
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type tunnelTrafficSample struct {
	iface  string
	rx, tx int64
	at     time.Time
}

// TunnelTrafficHistory adds counter movement to failure evidence. These
// deltas include keepalives and therefore never override a failed data probe.
type TunnelTrafficHistory struct {
	mu   sync.Mutex
	last map[string]tunnelTrafficSample
}

func (h *TunnelTrafficHistory) Annotate(out []wire.Check, tunnels []awgmgr.Tunnel, now time.Time) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[string]tunnelTrafficSample, len(tunnels))
	byID := make(map[string]int, len(out))
	for i := range out {
		if id, ok := strings.CutPrefix(out[i].Name, tunnelCheckPrefix); ok {
			byID[id] = i
		}
	}
	for _, tu := range tunnels {
		next[tu.ID] = tunnelTrafficSample{iface: tu.InterfaceName, rx: tu.RxBytes, tx: tu.TxBytes, at: now}
		i, found := byID[tu.ID]
		if !found {
			continue
		}
		old, ok := h.last[tu.ID]
		if !ok || old.iface != tu.InterfaceName || !now.After(old.at) {
			continue
		}
		if out[i].Details == nil {
			out[i].Details = map[string]any{}
		}
		d := out[i].Details
		if tu.RxBytes < old.rx || tu.TxBytes < old.tx {
			d["traffic_counters_reset"] = true
			continue
		}
		d["rx_delta_bytes"], d["tx_delta_bytes"] = tu.RxBytes-old.rx, tu.TxBytes-old.tx
		d["traffic_sample_ms"] = now.Sub(old.at).Milliseconds()
	}
	h.last = next
}
