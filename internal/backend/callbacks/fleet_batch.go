package callbacks

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fleetBatchStore struct {
	mu      sync.Mutex
	batches map[string]*fleetBatch
	// senders holds one coalescing sender per BulkID (C4): concurrent
	// NotifyBulkCommandResult calls for the same batch must not each fire
	// their own EditMessageText — see batchSender.
	senders map[string]*batchSender
}

type fleetBatch struct {
	ID        string
	Title     string
	Action    string
	ChatID    int64
	MessageID int64
	ThreadID  *int64
	CreatedAt time.Time
	Items     map[string]*fleetBatchItem
	Order     []string
}

type fleetBatchItem struct {
	Nick    string
	Status  string
	Summary string
}

func newFleetBatchStore() *fleetBatchStore {
	return &fleetBatchStore{batches: make(map[string]*fleetBatch), senders: make(map[string]*batchSender)}
}

func (s *fleetBatchStore) start(b *fleetBatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches[b.ID] = b
}

func (s *fleetBatchStore) update(batchID, nick, status, summary string) (*fleetBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[batchID]
	if !ok {
		return nil, false
	}
	item, ok := b.Items[nick]
	if !ok {
		item = &fleetBatchItem{Nick: nick}
		b.Items[nick] = item
		b.Order = append(b.Order, nick)
	}
	item.Status = status
	item.Summary = summary
	cp := cloneFleetBatch(b)
	return cp, true
}

// snapshot returns a point-in-time clone of the live batch state. Used by
// batchSender.sendCoalesced to always render the freshest data at actual
// send time, rather than a copy captured back when some earlier caller's own
// update() ran — which could already be stale by the time its turn to
// (re-)send comes up under concurrent updates (C4).
func (s *fleetBatchStore) snapshot(batchID string) (*fleetBatch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.batches[batchID]
	if !ok {
		return nil, false
	}
	return cloneFleetBatch(b), true
}

// senderFor returns the coalescing sender for batchID, creating one on first
// use. One sender per BulkID for the lifetime of the process — mirrors
// batches' own never-reaped lifecycle (fleet batches are created only by
// operator-triggered fleet commands, not per-agent traffic, so this stays
// small and bounded).
func (s *fleetBatchStore) senderFor(batchID string) *batchSender {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd, ok := s.senders[batchID]
	if !ok {
		sd = &batchSender{}
		s.senders[batchID] = sd
	}
	return sd
}

// batchSender coalesces concurrent edits for a single fleet BulkID into at
// most one in-flight Telegram EditMessageText call, with at most one
// trailing send queued behind it (C4). Multiple agents in a fleet-wide
// command can report results back within milliseconds of each other;
// without this, each result raced its own EditMessageText call directly off
// the map snapshot it read outside any lock, so a slower network round trip
// for an EARLIER snapshot could land after a later one and show stale
// progress (or flap) in the TG message.
//
// The first concurrent caller becomes the "active" sender and loops
// re-rendering + sending the latest snapshot until no update arrived during
// its last send; every other concurrent caller just marks the sender dirty
// and returns immediately — the active sender's next loop iteration
// re-fetches the live snapshot (via get) and delivers it, so the final edit
// always reflects the newest state regardless of which goroutine's update()
// happened to run last.
type batchSender struct {
	mu      sync.Mutex
	sending bool
	dirty   bool
}

// sendCoalesced ensures at most one send (network call) is in flight at a
// time for this sender. get must return the freshest known snapshot (safe to
// call repeatedly/concurrently — see fleetBatchStore.snapshot). If another
// goroutine is already sending, this call marks it dirty and returns nil
// immediately without touching the network; the active sender is guaranteed
// to loop at least once more before it stops, so the update is never lost.
//
// ctx is the caller-that-became-the-active-sender's context and is reused
// for every loop iteration (including any trailing coalesced sends) — the
// active sender's own goroutine doesn't return, and hence its ctx isn't
// cancelled, until the whole loop finishes.
func (s *batchSender) sendCoalesced(ctx context.Context, get func() *fleetBatch, send func(context.Context, *fleetBatch) error) error {
	s.mu.Lock()
	if s.sending {
		s.dirty = true
		s.mu.Unlock()
		return nil
	}
	s.sending = true
	s.mu.Unlock()

	var firstErr error
	for {
		if snap := get(); snap != nil {
			if err := send(ctx, snap); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		s.mu.Lock()
		if !s.dirty {
			s.sending = false
			s.mu.Unlock()
			return firstErr
		}
		s.dirty = false
		s.mu.Unlock()
	}
}

func cloneFleetBatch(b *fleetBatch) *fleetBatch {
	cp := *b
	if b.ThreadID != nil {
		v := *b.ThreadID
		cp.ThreadID = &v
	}
	cp.Order = append([]string(nil), b.Order...)
	cp.Items = make(map[string]*fleetBatchItem, len(b.Items))
	for k, v := range b.Items {
		item := *v
		cp.Items[k] = &item
	}
	return &cp
}

func (b *fleetBatch) render() string {
	var queued, ok, failed, skipped int
	for _, nick := range b.Order {
		switch b.Items[nick].Status {
		case "ok":
			ok++
		case "failed":
			failed++
		case "skipped":
			skipped++
		default:
			queued++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Fleet: %s\n\n", b.Title)
	fmt.Fprintf(&out, "Summary: ok: %d, failed: %d, queued: %d, skipped: %d\n", ok, failed, queued, skipped)
	out.WriteString("This report will update here as routers answer.\n\n")
	for _, nick := range b.Order {
		item := b.Items[nick]
		icon := "..."
		switch item.Status {
		case "ok":
			icon = "OK"
		case "failed":
			icon = "ERR"
		case "skipped":
			icon = "SKIP"
		}
		line := strings.TrimSpace(item.Summary)
		if line == "" {
			line = "waiting"
		}
		fmt.Fprintf(&out, "- %s %s: %s\n", icon, item.Nick, oneLine(line, 180))
	}
	text := out.String()
	if len(text) > 4096 {
		text = text[:4093] + "..."
	}
	return text
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

func (r *Router) NotifyBulkCommandResult(ctx context.Context, ref cmdpkg.MessageRef, res wire.CommandResult, userID int64) error {
	nick := ref.BulkNick
	if nick == "" {
		if u, err := r.d.Users().GetByID(userID); err == nil && u != nil {
			nick = u.Nickname
		} else {
			nick = fmt.Sprintf("user-%d", userID)
		}
	}
	status := "failed"
	if res.Status == "ok" {
		status = "ok"
	}
	summary := res.Output
	if summary == "" {
		summary = res.Status
	}
	if _, ok := r.fleetBatches.update(ref.BulkID, nick, status, summary); !ok {
		return fmt.Errorf("fleet batch not found: %s", ref.BulkID)
	}
	sender := r.fleetBatches.senderFor(ref.BulkID)
	return sender.sendCoalesced(ctx,
		func() *fleetBatch {
			b, _ := r.fleetBatches.snapshot(ref.BulkID)
			return b
		},
		func(sendCtx context.Context, snap *fleetBatch) error {
			kb := panelResultKb()
			return r.tg.EditMessageText(sendCtx, snap.ChatID, snap.MessageID, snap.render(), "", &kb)
		},
	)
}

func newFleetBatchID() string {
	return "fleet-" + defaultCmdID()
}

func fleetCommand(action string, args map[string]any) wire.Command {
	return wire.Command{
		ID:       defaultCmdID(),
		Action:   action,
		Args:     args,
		IssuedAt: time.Now().UTC(),
	}
}

func isSafeSelfUpdateVersion(version string) bool {
	if !strings.Contains(version, "-rc") {
		cmp, ok := compareReleaseTagsLocal(version, "v0.13.0")
		return ok && cmp >= 0
	}
	cmp, ok := compareReleaseTagsLocal(version, "v0.13.0-rc32")
	return ok && cmp >= 0
}

func compareReleaseTagsLocal(a, b string) (int, bool) {
	aa, aok := releaseTagRankLocal(a)
	bb, bok := releaseTagRankLocal(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range aa {
		if aa[i] < bb[i] {
			return -1, true
		}
		if aa[i] > bb[i] {
			return 1, true
		}
	}
	return 0, true
}

func releaseTagRankLocal(v string) ([4]int, bool) {
	var rank [4]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	main, rcRaw, hasRC := strings.Cut(v, "-rc")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return rank, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return rank, false
		}
		rank[i] = n
	}
	if !hasRC {
		rank[3] = 1 << 30
		return rank, true
	}
	rc, err := strconv.Atoi(rcRaw)
	if err != nil {
		return rank, false
	}
	rank[3] = rc
	return rank, true
}

var _ interface {
	NotifyBulkCommandResult(context.Context, cmdpkg.MessageRef, wire.CommandResult, int64) error
} = (*Router)(nil)
