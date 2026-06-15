package cmd

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func mkCmd(id, action string) wire.Command {
	return wire.Command{
		ID:       id,
		Action:   action,
		IssuedAt: time.Now().UTC(),
	}
}

func TestQueue_EnqueueDequeueImmediate(t *testing.T) {
	q := New()
	c := mkCmd("c1", "diag_now")
	if err := q.Enqueue(7, c); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, ok := q.Dequeue(context.Background(), 7, 10*time.Millisecond)
	if !ok {
		t.Fatalf("dequeue not ok")
	}
	if got.ID != "c1" {
		t.Errorf("id mismatch: %q", got.ID)
	}
}

func TestQueue_DequeueWaitsForEnqueue(t *testing.T) {
	q := New()
	var got *wire.Command
	var ok bool
	done := make(chan struct{})
	go func() {
		got, ok = q.Dequeue(context.Background(), 42, 500*time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	c := mkCmd("c-wakeup", "restart_tunnel")
	if err := q.Enqueue(42, c); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("dequeue never woke up")
	}
	if !ok || got == nil || got.ID != "c-wakeup" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}

func TestQueue_DequeueCtxCancel(t *testing.T) {
	q := New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var ok bool
	go func() {
		_, ok = q.Dequeue(ctx, 1, time.Hour)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("dequeue did not unblock on ctx cancel")
	}
	if ok {
		t.Error("expected ok=false on ctx cancel")
	}
}

func TestQueue_DequeueHoldTimeout(t *testing.T) {
	q := New()
	start := time.Now()
	got, ok := q.Dequeue(context.Background(), 1, 30*time.Millisecond)
	if ok || got != nil {
		t.Errorf("expected (nil,false) on timeout, got (%+v,%v)", got, ok)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
}

func TestQueue_FIFO(t *testing.T) {
	q := New()
	for _, id := range []string{"a", "b", "c"} {
		_ = q.Enqueue(99, mkCmd(id, "diag_now"))
	}
	for _, want := range []string{"a", "b", "c"} {
		got, ok := q.Dequeue(context.Background(), 99, 10*time.Millisecond)
		if !ok || got.ID != want {
			t.Errorf("FIFO step want=%q got=%+v ok=%v", want, got, ok)
		}
	}
}

func TestQueue_DequeueSkipsExpiredCommands(t *testing.T) {
	q := New()
	expired := mkCmd("old", "diag_now")
	expired.ExpiresAt = time.Now().Add(-time.Second)
	fresh := mkCmd("fresh", "diag_now")
	fresh.ExpiresAt = time.Now().Add(time.Minute)

	if err := q.Enqueue(7, expired); err != nil {
		t.Fatalf("enqueue expired: %v", err)
	}
	if err := q.Enqueue(7, fresh); err != nil {
		t.Fatalf("enqueue fresh: %v", err)
	}

	got, ok := q.Dequeue(context.Background(), 7, 10*time.Millisecond)
	if !ok || got == nil || got.ID != "fresh" {
		t.Fatalf("want fresh command after expired skip, got %+v ok=%v", got, ok)
	}
}

func TestQueue_EnqueueSupersedesSelfUpdate(t *testing.T) {
	q := New()
	first := mkCmd("old-update", "self_update")
	second := mkCmd("new-update", "self_update")
	if err := q.Enqueue(7, first); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := q.Enqueue(7, second); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	got, ok := q.Dequeue(context.Background(), 7, 10*time.Millisecond)
	if !ok || got == nil || got.ID != "new-update" {
		t.Fatalf("self_update should keep latest only, got %+v ok=%v", got, ok)
	}
	if got, ok = q.Dequeue(context.Background(), 7, 10*time.Millisecond); ok || got != nil {
		t.Fatalf("old self_update should be removed, got %+v ok=%v", got, ok)
	}
}

func TestQueue_EnqueueSupersedesSelfUpdateOrigin(t *testing.T) {
	q := New()
	first := mkCmd("old-update", "self_update")
	second := mkCmd("new-update", "self_update")
	if err := q.EnqueueWithRef(7, first, MessageRef{ChatID: 1, MessageID: 10}); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	if err := q.EnqueueWithRef(7, second, MessageRef{ChatID: 1, MessageID: 11}); err != nil {
		t.Fatalf("enqueue second: %v", err)
	}

	if _, ok := q.OriginRef(7, "old-update"); ok {
		t.Fatal("superseded self_update must not keep a stale origin ref")
	}
	if ref, ok := q.OriginRef(7, "new-update"); !ok || ref.MessageID != 11 {
		t.Fatalf("new self_update origin missing or wrong: %+v ok=%v", ref, ok)
	}
}

func TestQueue_CommandByIDReturnsDequeuedCommand(t *testing.T) {
	q := New()
	cmd := wire.Command{
		ID:     "update-1",
		Action: "self_update",
		Args:   map[string]any{"version": "v0.13.0-rc53"},
	}
	if err := q.Enqueue(7, cmd); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, ok := q.CommandByID(7, "update-1"); ok {
		t.Fatal("command must not be visible before agent dequeues it")
	}
	if _, ok := q.Dequeue(context.Background(), 7, 10*time.Millisecond); !ok {
		t.Fatal("dequeue failed")
	}
	got, ok := q.CommandByID(7, "update-1")
	if !ok {
		t.Fatal("command not found after dequeue")
	}
	if got.Action != "self_update" || got.Args["version"] != "v0.13.0-rc53" {
		t.Fatalf("bad command: %+v", got)
	}
}

func TestQueue_MultiUserIsolation(t *testing.T) {
	q := New()
	_ = q.Enqueue(1, mkCmd("u1-cmd", "diag_now"))
	_ = q.Enqueue(2, mkCmd("u2-cmd", "pingcheck_now"))

	got1, ok1 := q.Dequeue(context.Background(), 1, 10*time.Millisecond)
	got2, ok2 := q.Dequeue(context.Background(), 2, 10*time.Millisecond)
	if !ok1 || got1.ID != "u1-cmd" {
		t.Errorf("user1: got %+v ok=%v", got1, ok1)
	}
	if !ok2 || got2.ID != "u2-cmd" {
		t.Errorf("user2: got %+v ok=%v", got2, ok2)
	}
}

func TestQueue_EnqueueRejectsEmptyID(t *testing.T) {
	q := New()
	err := q.Enqueue(1, wire.Command{Action: "diag_now"})
	if err == nil {
		t.Fatal("expected error on empty Command.ID")
	}
}

func TestQueue_EnqueueRejectsInvalidAction(t *testing.T) {
	q := New()
	err := q.Enqueue(1, wire.Command{ID: "x", Action: "rm-rf"})
	if err == nil {
		t.Fatal("expected error on invalid action")
	}
}

func TestQueue_RecordAndAwaitResult(t *testing.T) {
	q := New()
	issueCommandForTest(t, q, 7, wire.Command{ID: "c1", Action: "diag_now"})
	res := wire.CommandResult{ID: "c1", Status: "ok", DurationMs: 12}
	if err := q.RecordResult(7, res); err != nil {
		t.Fatalf("record: %v", err)
	}
	got, ok := q.AwaitResult(context.Background(), 7, "c1", 50*time.Millisecond)
	if !ok || got == nil || got.Status != "ok" || got.DurationMs != 12 {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}

func TestQueue_AwaitResultWaitsForLateRecord(t *testing.T) {
	q := New()
	issueCommandForTest(t, q, 7, wire.Command{ID: "late", Action: "diag_now"})
	var got *wire.CommandResult
	var ok bool
	done := make(chan struct{})
	go func() {
		got, ok = q.AwaitResult(context.Background(), 7, "late", 500*time.Millisecond)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	_ = q.RecordResult(7, wire.CommandResult{ID: "late", Status: "ok"})
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("await never woke up")
	}
	if !ok || got == nil || got.ID != "late" {
		t.Errorf("got=%+v ok=%v", got, ok)
	}
}

func TestQueue_AwaitResultTimeout(t *testing.T) {
	q := New()
	got, ok := q.AwaitResult(context.Background(), 7, "never", 30*time.Millisecond)
	if ok || got != nil {
		t.Errorf("expected (nil,false), got (%+v,%v)", got, ok)
	}
}

func TestQueue_RecordResultRejectsUnissuedCommand(t *testing.T) {
	q := New()
	err := q.RecordResult(7, wire.CommandResult{ID: "ghost", Status: "ok"})
	if err == nil {
		t.Fatal("expected unissued command result to be rejected")
	}
	if got, ok := q.AwaitResult(context.Background(), 7, "ghost", 10*time.Millisecond); ok || got != nil {
		t.Fatalf("unissued result should not be awaitable, got=%+v ok=%v", got, ok)
	}
}

// TestQueue_RecordResultAcceptsUnknownStatus pins forward-compat: queue
// stores results with unknown statuses (logging is the caller's concern).
// Empty status remains a hard error (real client bug, not schema evolution).
func TestQueue_RecordResultAcceptsUnknownStatus(t *testing.T) {
	q := New()
	issueCommandForTest(t, q, 1, wire.Command{ID: "x", Action: "diag_now"})
	if err := q.RecordResult(1, wire.CommandResult{ID: "x", Status: "weird"}); err != nil {
		t.Errorf("expected unknown status to be accepted, got %v", err)
	}
	if err := q.RecordResult(1, wire.CommandResult{ID: "y", Status: ""}); err == nil {
		t.Errorf("expected empty status to be rejected")
	}
}

func TestQueue_RecordResultKeepsFirstResult(t *testing.T) {
	q := New()
	issueCommandForTest(t, q, 1, wire.Command{ID: "cmd1", Action: "diag_now"})
	if err := q.RecordResult(1, wire.CommandResult{ID: "cmd1", Status: "err", Output: "first"}); err != nil {
		t.Fatalf("record first: %v", err)
	}
	if err := q.RecordResult(1, wire.CommandResult{ID: "cmd1", Status: "ok", Output: "second"}); !errors.Is(err, ErrDuplicateResult) {
		t.Fatalf("record duplicate err=%v, want ErrDuplicateResult", err)
	}
	got, ok := q.AwaitResult(context.Background(), 1, "cmd1", 10*time.Millisecond)
	if !ok || got == nil {
		t.Fatal("result missing")
	}
	if got.Status != "err" || got.Output != "first" {
		t.Fatalf("duplicate result overwrote first result: %+v", got)
	}
}

func issueCommandForTest(t *testing.T, q *Queue, userID int64, cmd wire.Command) {
	t.Helper()
	if err := q.Enqueue(userID, cmd); err != nil {
		t.Fatalf("enqueue issued command: %v", err)
	}
	got, ok := q.Dequeue(context.Background(), userID, time.Second)
	if !ok || got == nil || got.ID != cmd.ID {
		t.Fatalf("dequeue issued command got=%+v ok=%v", got, ok)
	}
}

func TestQueueEnqueueWithRefAndLookup(t *testing.T) {
	q := New()
	tid := int64(11)
	ref := MessageRef{ChatID: -100, MessageID: 99, ThreadID: &tid}
	cmd := wire.Command{ID: "abc", Action: "diag_now", IssuedAt: time.Now()}
	if err := q.EnqueueWithRef(7, cmd, ref); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	got, ok := q.OriginRef(7, "abc")
	if !ok {
		t.Fatalf("OriginRef miss")
	}
	if got.ChatID != -100 || got.MessageID != 99 || got.ThreadID == nil || *got.ThreadID != 11 {
		t.Errorf("ref mismatch: %+v", got)
	}
	// Action must be copied from cmd.Action (consumed in T16 by relay).
	if got.Action != "diag_now" {
		t.Errorf("Action not copied: %q", got.Action)
	}
	// Bare Enqueue still works and does NOT record a ref.
	if err := q.Enqueue(7, wire.Command{ID: "no-ref", Action: "diag_now", IssuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, ok := q.OriginRef(7, "no-ref"); ok {
		t.Errorf("bare Enqueue should NOT record ref")
	}
}

func TestQueueConsumeOriginRefDeletes(t *testing.T) {
	q := New()
	cmd := wire.Command{ID: "once", Action: "diag_now", IssuedAt: time.Now()}
	ref := MessageRef{ChatID: -100, MessageID: 50}
	_ = q.EnqueueWithRef(3, cmd, ref)
	got, ok := q.ConsumeOriginRef(3, "once")
	if !ok || got.MessageID != 50 {
		t.Fatalf("first consume miss: %+v ok=%v", got, ok)
	}
	if _, ok := q.ConsumeOriginRef(3, "once"); ok {
		t.Errorf("second consume should miss after delete")
	}
}

// Race-test: many goroutines enqueueing concurrently — no deadlocks, no data race.
func TestQueue_ConcurrentEnqueue(t *testing.T) {
	q := New()
	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = q.Enqueue(123, mkCmd(string(rune('A'+i%26))+"-"+string(rune('a'+i%26)), "diag_now"))
		}(i)
	}
	wg.Wait()
	count := 0
	for {
		_, ok := q.Dequeue(context.Background(), 123, 5*time.Millisecond)
		if !ok {
			break
		}
		count++
	}
	if count != N {
		t.Errorf("expected %d items dequeued, got %d", N, count)
	}
}
