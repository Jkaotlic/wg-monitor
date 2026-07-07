package callbacks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

// coalesceRecorderTG is a minimal TGClient fake for fleet-batch coalescing
// tests (C4). EditMessageText optionally blocks the very first call on
// blockFirst so a test can force concurrent NotifyBulkCommandResult calls to
// pile up behind an in-flight "network" send, and tracks whether any two
// calls were ever concurrently inside EditMessageText — which the coalescing
// fix must prevent (fleet-batch edits must serialize per BulkID).
type coalesceRecorderTG struct {
	mu           sync.Mutex
	texts        []string
	active       int
	maxActive    int
	blockClaimed bool // true once some call has claimed the "first call" gate below

	blockFirst   chan struct{} // if non-nil, first call blocks here until closed
	firstEntered chan struct{} // if non-nil, closed once the first call starts blocking
}

func (f *coalesceRecorderTG) EditMessageText(_ context.Context, _, _ int64, text, _ string, _ *tg.InlineKeyboardMarkup) error {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	// Claim the "first call" gate under the SAME lock used for the active
	// counter, via a plain bool — NOT sync.Once. Once.Do blocks every
	// concurrent caller until the first caller's function returns, which
	// would deadlock this test: the other N calls must return immediately
	// (uncontended) so the test can observe they were coalesced away rather
	// than each blocking on the gate themselves.
	isFirst := !f.blockClaimed
	if isFirst {
		f.blockClaimed = true
	}
	f.mu.Unlock()

	if isFirst {
		if f.firstEntered != nil {
			close(f.firstEntered)
		}
		if f.blockFirst != nil {
			<-f.blockFirst
		}
	}

	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.active--
	f.mu.Unlock()
	return nil
}

func (f *coalesceRecorderTG) snapshot() (texts []string, maxActive int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...), f.maxActive
}

// Remaining TGClient methods are unused no-ops — this fake only exercises
// fleet-batch NotifyBulkCommandResult (EditMessageText).
func (f *coalesceRecorderTG) SendMessage(context.Context, int64, *int64, string, string, *int64) (int64, error) {
	return 0, nil
}
func (f *coalesceRecorderTG) SendMessageWithReplyKeyboard(context.Context, int64, *int64, string, string, *int64, any) (int64, error) {
	return 0, nil
}
func (f *coalesceRecorderTG) DeleteMessage(context.Context, int64, int64) error         { return nil }
func (f *coalesceRecorderTG) AnswerCallbackQuery(context.Context, string, string) error { return nil }
func (f *coalesceRecorderTG) GetUpdates(context.Context, int64, int) ([]tg.Update, error) {
	return nil, nil
}
func (f *coalesceRecorderTG) GetFile(context.Context, string) (string, error) { return "", nil }
func (f *coalesceRecorderTG) DownloadFile(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (f *coalesceRecorderTG) CreateForumTopic(context.Context, int64, string, int) (int64, error) {
	return 0, nil
}

var _ TGClient = (*coalesceRecorderTG)(nil)

// TestNotifyBulkCommandResult_RendersAndEditsSingleUpdate is a basic
// correctness check (this file had no coverage of NotifyBulkCommandResult
// before C4): a single result updates the batch and edits the TG message.
func TestNotifyBulkCommandResult_RendersAndEditsSingleUpdate(t *testing.T) {
	d, uid := newTestDB(t)
	rec := &coalesceRecorderTG{}
	r := NewRouter(d, rec, Config{})
	batch := &fleetBatch{ID: "fleet-1", Title: "single", ChatID: 10, MessageID: 20, Items: make(map[string]*fleetBatchItem)}
	r.fleetBatches.start(batch)

	ref := cmdpkg.MessageRef{BulkID: "fleet-1", BulkNick: "vasya"}
	if err := r.NotifyBulkCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: "done"}, uid); err != nil {
		t.Fatal(err)
	}
	texts, _ := rec.snapshot()
	if len(texts) != 1 {
		t.Fatalf("expected exactly 1 edit, got %d: %v", len(texts), texts)
	}
	if !strings.Contains(texts[0], "vasya") || !strings.Contains(texts[0], "done") {
		t.Errorf("rendered text missing update: %s", texts[0])
	}
}

// TestNotifyBulkCommandResult_UnknownBatchReturnsError pins the not-found
// error path: no TG call must happen for a BulkID the store never started.
func TestNotifyBulkCommandResult_UnknownBatchReturnsError(t *testing.T) {
	d, uid := newTestDB(t)
	rec := &coalesceRecorderTG{}
	r := NewRouter(d, rec, Config{})
	ref := cmdpkg.MessageRef{BulkID: "does-not-exist", BulkNick: "vasya"}
	if err := r.NotifyBulkCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok"}, uid); err == nil {
		t.Fatal("expected error for unknown BulkID")
	}
	if texts, _ := rec.snapshot(); len(texts) != 0 {
		t.Errorf("expected no edit for unknown batch, got %v", texts)
	}
}

// TestNotifyBulkCommandResult_CoalescesConcurrentEdits pins C4: concurrent
// NotifyBulkCommandResult calls for the SAME BulkID must serialize their
// EditMessageText calls (never concurrently in flight) and coalesce down to
// at most one trailing send that reflects the newest state — not a separate
// network call per result, which can race and land out of order.
func TestNotifyBulkCommandResult_CoalescesConcurrentEdits(t *testing.T) {
	d, uid := newTestDB(t)
	rec := &coalesceRecorderTG{
		blockFirst:   make(chan struct{}),
		firstEntered: make(chan struct{}),
	}
	r := NewRouter(d, rec, Config{})
	batch := &fleetBatch{ID: "fleet-race", Title: "race", ChatID: 1, MessageID: 2, Items: make(map[string]*fleetBatchItem)}
	r.fleetBatches.start(batch)

	// Kick off the first update; its EditMessageText call blocks until
	// released below, simulating a slow Telegram round trip that the other
	// N concurrent results arrive during.
	firstDone := make(chan error, 1)
	go func() {
		ref := cmdpkg.MessageRef{BulkID: "fleet-race", BulkNick: "agent-0"}
		firstDone <- r.NotifyBulkCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: "0"}, uid)
	}()
	<-rec.firstEntered // first send is now blocked inside EditMessageText

	const n = 20
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref := cmdpkg.MessageRef{BulkID: "fleet-race", BulkNick: fmt.Sprintf("agent-%d", i)}
			res := wire.CommandResult{Status: "ok", Output: fmt.Sprintf("%d", i)}
			if err := r.NotifyBulkCommandResult(context.Background(), ref, res, uid); err != nil {
				t.Errorf("NotifyBulkCommandResult(agent-%d): %v", i, err)
			}
		}(i)
	}
	// All N concurrent calls must return (coalesced away) WITHOUT waiting
	// for the blocked first send — proving they didn't each fire their own
	// EditMessageText.
	wg.Wait()

	close(rec.blockFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first NotifyBulkCommandResult: %v", err)
	}

	texts, maxActive := rec.snapshot()
	if maxActive > 1 {
		t.Fatalf("EditMessageText re-entered concurrently: maxActive=%d, want <=1 (concurrent fleet-batch edits must serialize per BulkID)", maxActive)
	}
	if len(texts) > 2 {
		t.Fatalf("expected coalescing to cap sends at 2 (in-flight + one trailing), got %d sends: %v", len(texts), texts)
	}
	if len(texts) == 0 {
		t.Fatal("expected at least one edit to have been sent")
	}
	last := texts[len(texts)-1]
	wantAgent := fmt.Sprintf("agent-%d", n)
	if !strings.Contains(last, wantAgent) {
		t.Errorf("last edit should reflect the most recent update (%s), got:\n%s", wantAgent, last)
	}
	if wantSummary := fmt.Sprintf("ok: %d,", n+1); !strings.Contains(last, wantSummary) {
		t.Errorf("last edit should summarize all %d updates (%q), got:\n%s", n+1, wantSummary, last)
	}
}

func TestIsSafeSelfUpdateVersionRejectsMalformedVersions(t *testing.T) {
	for _, version := range []string{"v0.13.foo", "v0.13.0-rcx", "not-a-version"} {
		if isSafeSelfUpdateVersion(version) {
			t.Fatalf("malformed version %q should not be safe for fleet self-update", version)
		}
	}
}

func TestIsSafeSelfUpdateVersionKeepsKnownBoundaries(t *testing.T) {
	if !isSafeSelfUpdateVersion("v0.13.0-rc32") {
		t.Fatal("rc32 should be safe")
	}
	if isSafeSelfUpdateVersion("v0.13.0-rc31") {
		t.Fatal("rc31 should be unsafe")
	}
	if !isSafeSelfUpdateVersion("v0.13.0") {
		t.Fatal("stable v0.13.0 should be safe")
	}
}
