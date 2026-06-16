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
	return &fleetBatchStore{batches: make(map[string]*fleetBatch)}
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
	b, ok := r.fleetBatches.update(ref.BulkID, nick, status, summary)
	if !ok {
		return fmt.Errorf("fleet batch not found: %s", ref.BulkID)
	}
	kb := panelResultKb()
	return r.tg.EditMessageText(ctx, b.ChatID, b.MessageID, b.render(), "", &kb)
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
