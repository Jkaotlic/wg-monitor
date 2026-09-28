// Package facts собирает блок wire.ReportFacts для отчёта: каждый вид
// уходит, когда изменился или раз в Refresh (подтверждение свежести), а
// серии пингчека -- всегда, пока бэкенд их не принял.
package facts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const defaultRefresh = 10 * time.Minute

// PingSource -- pingruns.Tracker.
type PingSource interface {
	Poll(ctx context.Context)
	Pending() []wire.PingRun
	Commit(sent []wire.PingRun)
	Log() *wire.PingLogFacts
}

type Hub struct {
	Exit      func() *wire.ExitFacts
	Ping      PingSource
	WAN       func(ctx context.Context) *wire.WANFacts
	NativeDNS func(ctx context.Context) *wire.NativeDNSFacts
	Hooks     func() *wire.HookFacts
	Now       func() time.Time
	Refresh   time.Duration

	mu       sync.Mutex
	sent     map[string]mark
	inflight map[string]string
}

type mark struct {
	hash string
	at   time.Time
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now().UTC()
}

func hashOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// due -- слать ли блок: изменился или не подтверждался дольше Refresh.
func (h *Hub) due(kind string, v any, now time.Time) bool {
	refresh := h.Refresh
	if refresh <= 0 {
		refresh = defaultRefresh
	}
	hash := hashOf(v)
	if m, ok := h.sent[kind]; ok && m.hash == hash && now.Sub(m.at) < refresh {
		return false
	}
	h.inflight[kind] = hash
	return true
}

func (h *Hub) Collect(ctx context.Context) *wire.ReportFacts {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sent == nil {
		h.sent = map[string]mark{}
	}
	h.inflight = map[string]string{}
	now := h.now()
	f := &wire.ReportFacts{}
	if h.Exit != nil {
		if b := h.Exit(); b != nil && h.due("exit", b, now) {
			f.Exit = b
		}
	}
	if h.WAN != nil {
		if b := h.WAN(ctx); b != nil && h.due("wan", b, now) {
			// P7: b может быть тем же указателем, что держит wanfacts.Collector
			// в своём кеше (Fact() отдаёт c.cached напрямую, пока не истёк
			// Every). Clamp режет Links на месте -- без копии это укоротило бы
			// кеш коллектора навсегда, а не только уходящий в отчёт блок.
			bc := *b
			bc.Links = slices.Clone(b.Links)
			f.WAN = &bc
		}
	}
	if h.NativeDNS != nil {
		if b := h.NativeDNS(ctx); b != nil && h.due("native_dns", b, now) {
			// P7: та же защита на будущий кеширующий коллектор native_dns.
			bc := *b
			bc.Lists = slices.Clone(b.Lists)
			f.NativeDNS = &bc
		}
	}
	if h.Hooks != nil {
		if b := h.Hooks(); b != nil && h.due("hooks", b, now) {
			f.Hooks = b
		}
	}
	if h.Ping != nil {
		h.Ping.Poll(ctx)
		f.PingRuns = h.Ping.Pending()
		if b := h.Ping.Log(); b != nil && h.due("ping_log", b, now) {
			f.PingLog = b
		}
	}
	f.Clamp()
	if f.Empty() {
		return nil
	}
	return f
}

// Committed -- отчёт принят: отправленные виды помечаются, серии подтверждаются.
func (h *Hub) Committed(sent *wire.ReportFacts) {
	h.mu.Lock()
	now := h.now()
	for kind, hash := range h.inflight {
		h.sent[kind] = mark{hash: hash, at: now}
	}
	h.inflight = nil
	h.mu.Unlock()
	if h.Ping != nil && sent != nil && len(sent.PingRuns) > 0 {
		h.Ping.Commit(sent.PingRuns)
	}
}
