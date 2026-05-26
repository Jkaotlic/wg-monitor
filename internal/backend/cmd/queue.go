// Package cmd is the in-memory command channel between backend and agents.
//
// Lifetime: a TG admin taps a button → callbacks.Router enqueues a wire.Command
// for that user → agent's long-poll GET /v1/cmd dequeues → agent runs the
// action → POST /v1/cmd/result lands here. Backend restart drops the queue;
// admin re-taps the button if needed (acceptable: no command persists money
// or external state until the agent runs it).
package cmd

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/anex/wg-monitor/pkg/wire"
)

// MessageRef identifies the originating TG message for a command — chat, the
// message that carried the inline button, the optional topic, and the action
// name (copied from cmd.Action so the result-handler can format the relay
// without re-fetching the queued command).
type MessageRef struct {
	ChatID    int64
	MessageID int64
	ThreadID  *int64
	Action    string
	BulkID    string
	BulkNick  string
}

// resultEntry / originEntry pair their payload with a creation timestamp so
// the Sweep janitor can evict stale entries (LOGIC-02). Without this both
// maps grew without bound for the lifetime of the backend process.
type resultEntry struct {
	result     wire.CommandResult
	recordedAt time.Time
}

type originEntry struct {
	ref        MessageRef
	enqueuedAt time.Time
}

type commandEntry struct {
	cmd      wire.Command
	issuedAt time.Time
}

// Queue is per-user FIFO queues plus a per-(user,id) result map plus a
// per-(user,id) origin map. Concurrent-safe. Single mutex is fine —
// operations are short and the fleet is ~10 users, not 10k.
type Queue struct {
	mu      sync.Mutex
	pending map[int64][]wire.Command // userID → FIFO
	results map[int64]map[string]resultEntry
	issued  map[int64]map[string]commandEntry
	// origins maps (userID → cmd.ID → originEntry). Populated by
	// EnqueueWithRef; consumed by the cmd-result handler to relay TG replies.
	origins map[int64]map[string]originEntry
	signal  *sync.Cond   // signals on Enqueue and RecordResult
	logger  *slog.Logger // optional; nil → slog.Default()
}

// SetLogger overrides the queue's structured logger. Used by main to inject
// the JSON-handler with `component=cmd_queue` (OBS-08). Tests skip this.
func (q *Queue) SetLogger(l *slog.Logger) {
	q.logger = l
}

func (q *Queue) log() *slog.Logger {
	if q.logger != nil {
		return q.logger
	}
	return slog.Default()
}

func New() *Queue {
	q := &Queue{
		pending: make(map[int64][]wire.Command),
		results: make(map[int64]map[string]resultEntry),
		issued:  make(map[int64]map[string]commandEntry),
		origins: make(map[int64]map[string]originEntry),
	}
	q.signal = sync.NewCond(&q.mu)
	return q
}

func defaultCommandTTL(action string) time.Duration {
	switch action {
	case "self_update":
		return 30 * time.Minute
	case "firmware_install", "service_restart":
		return 10 * time.Minute
	default:
		return 15 * time.Minute
	}
}

func commandExpired(cmd wire.Command, now time.Time) bool {
	return !cmd.ExpiresAt.IsZero() && !now.Before(cmd.ExpiresAt)
}

func supersedesPending(action string) bool {
	return action == "self_update"
}

// EnqueueWithRef is Enqueue + records MessageRef (with ref.Action populated
// from cmd.Action) so that when the agent posts CommandResult later, the
// backend can reply to the original message. Bare Enqueue does not touch
// origins.
func (q *Queue) EnqueueWithRef(userID int64, cmd wire.Command, ref MessageRef) error {
	if err := q.Enqueue(userID, cmd); err != nil {
		return err
	}
	ref.Action = cmd.Action
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		bucket = make(map[string]originEntry)
		q.origins[userID] = bucket
	}
	bucket[cmd.ID] = originEntry{ref: ref, enqueuedAt: time.Now()}
	return nil
}

// OriginRef returns the MessageRef stored at EnqueueWithRef time, or
// (zero, false) if the command was enqueued without ref or already consumed.
func (q *Queue) OriginRef(userID int64, cmdID string) (MessageRef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		return MessageRef{}, false
	}
	r, ok := bucket[cmdID]
	return r.ref, ok
}

// ConsumeOriginRef is OriginRef + delete in one shot. Use from the result
// handler so the same ref isn't relayed twice if RecordResult somehow fires
// twice for the same command id. Result-map cleanup is handled separately by
// Sweep so AwaitResult-using callers (tests, future inline-toast UIs) can
// still read the result after the relay path has consumed the origin.
func (q *Queue) ConsumeOriginRef(userID int64, cmdID string) (MessageRef, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.origins[userID]
	if !ok {
		return MessageRef{}, false
	}
	r, ok := bucket[cmdID]
	if ok {
		delete(bucket, cmdID)
	}
	return r.ref, ok
}

// Enqueue appends cmd to userID's queue and wakes any waiter.
// Validates ID non-empty and Action whitelist.
func (q *Queue) Enqueue(userID int64, cmd wire.Command) error {
	if cmd.ID == "" {
		q.log().Warn("queue enqueue rejected", "reason", "id-empty", "user_id", userID, "action", cmd.Action)
		return errors.New("command id is required")
	}
	if !wire.IsValidCommandAction(cmd.Action) {
		q.log().Warn("queue enqueue rejected", "reason", "invalid-action", "user_id", userID, "action", cmd.Action)
		return errors.New("invalid command action: " + cmd.Action)
	}
	if cmd.IssuedAt.IsZero() {
		cmd.IssuedAt = time.Now().UTC()
	}
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = cmd.IssuedAt.Add(defaultCommandTTL(cmd.Action))
	}
	q.mu.Lock()
	if supersedesPending(cmd.Action) {
		filtered := q.pending[userID][:0]
		for _, existing := range q.pending[userID] {
			if existing.Action != cmd.Action {
				filtered = append(filtered, existing)
			}
		}
		q.pending[userID] = filtered
	}
	q.pending[userID] = append(q.pending[userID], cmd)
	pendingLen := len(q.pending[userID])
	q.mu.Unlock()
	q.signal.Broadcast()
	q.log().Debug("queue enqueue", "user_id", userID, "cmd_id", cmd.ID, "action", cmd.Action, "pending", pendingLen)
	return nil
}

// Dequeue returns the head command for userID. If empty, waits up to
// holdTimeout for an Enqueue or until ctx is done. Returns (nil, false) if
// timed out or ctx cancelled. Otherwise (cmd, true).
//
// Implemented via cond.Broadcast + tick-driven re-check rather than per-user
// channels: simpler, and the broadcast cost is negligible at fleet scale.
func (q *Queue) Dequeue(ctx context.Context, userID int64, holdTimeout time.Duration) (*wire.Command, bool) {
	deadline := time.Now().Add(holdTimeout)

	// Spawn a goroutine that wakes the cond when ctx is done or deadline hits,
	// so the cond.Wait below can unblock without a busy-loop. Use NewTimer +
	// Stop instead of time.After so the underlying timer is GC'd promptly when
	// the early-exit `stop` channel fires (PERF-05/BUG-21).
	stop := make(chan struct{})
	defer close(stop)
	timer := time.NewTimer(holdTimeout)
	defer timer.Stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-stop:
			return
		}
		q.signal.Broadcast()
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if cmds := q.pending[userID]; len(cmds) > 0 {
			head := cmds[0]
			// Realloc when slack >= 4× live entries to release the underlying
			// array (BUG-22). Stays an O(1) amortised pop in the steady-state
			// small-queue case.
			tail := cmds[1:]
			if len(tail) > 16 && cap(tail) > 4*len(tail) {
				fresh := make([]wire.Command, len(tail))
				copy(fresh, tail)
				tail = fresh
			}
			if len(tail) == 0 {
				delete(q.pending, userID)
			} else {
				q.pending[userID] = tail
			}
			if commandExpired(head, time.Now()) {
				q.log().Warn("queue drop expired command", "user_id", userID, "cmd_id", head.ID, "action", head.Action)
				if bucket, ok := q.origins[userID]; ok {
					delete(bucket, head.ID)
					if len(bucket) == 0 {
						delete(q.origins, userID)
					}
				}
				continue
			}
			bucket, ok := q.issued[userID]
			if !ok {
				bucket = make(map[string]commandEntry)
				q.issued[userID] = bucket
			}
			bucket[head.ID] = commandEntry{cmd: head, issuedAt: time.Now()}
			return &head, true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, false
		}
		q.signal.Wait()
	}
}

// RecordResult stores result under (userID, result.ID) and wakes any AwaitResult waiter.
//
// Status validation: пустой статус — отказ (явный bug в агенте). Любой
// non-empty статус принимаем — log-and-accept policy (см. handler.go).
// Whitelist enforcement (validCommandResultStatuses) был причиной потери
// данных при rolling-upgrade флота с новым агентом, эмитящим неизвестный
// backend'у статус.
func (q *Queue) RecordResult(userID int64, result wire.CommandResult) error {
	if result.ID == "" {
		q.log().Warn("queue record result rejected", "reason", "id-empty", "user_id", userID)
		return errors.New("result id is required")
	}
	if result.Status == "" {
		q.log().Warn("queue record result rejected", "reason", "status-empty", "user_id", userID, "cmd_id", result.ID)
		return errors.New("result status is required")
	}
	q.mu.Lock()
	bucket, ok := q.results[userID]
	if !ok {
		bucket = make(map[string]resultEntry)
		q.results[userID] = bucket
	}
	bucket[result.ID] = resultEntry{result: result, recordedAt: time.Now()}
	q.mu.Unlock()
	q.signal.Broadcast()
	q.log().Debug("queue record result", "user_id", userID, "cmd_id", result.ID, "status", result.Status)
	return nil
}

// CommandByID returns the command last dequeued for (userID, id). It lets the
// result handler recover action/args for commands enqueued without a Telegram
// origin ref, such as VPS deferred self_update jobs.
func (q *Queue) CommandByID(userID int64, cmdID string) (wire.Command, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	bucket, ok := q.issued[userID]
	if !ok {
		return wire.Command{}, false
	}
	entry, ok := bucket[cmdID]
	if !ok {
		return wire.Command{}, false
	}
	return entry.cmd, true
}

// AwaitResult blocks until RecordResult lands a matching (userID,id) entry,
// or the timeout/ctx-cancel hits. Useful for the TG callback handler that
// wants to display the action's outcome inline.
func (q *Queue) AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool) {
	deadline := time.Now().Add(timeout)
	stop := make(chan struct{})
	defer close(stop)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		select {
		case <-ctx.Done():
		case <-timer.C:
		case <-stop:
			return
		}
		q.signal.Broadcast()
	}()

	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if bucket, ok := q.results[userID]; ok {
			if r, found := bucket[id]; found {
				out := r.result
				return &out, true
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			slog.Debug("AwaitResult timeout", "user_id", userID, "cmd_id", id, "waited", timeout)
			return nil, false
		}
		q.signal.Wait()
	}
}

// Sweep evicts origin and result entries older than ttl. Caller drives the
// schedule (see cmd/backend/main.go); not auto-spawned to keep the package
// dependency-free of context plumbing. Returns the number of entries evicted.
func (q *Queue) Sweep(ttl time.Duration) (origins, results int) {
	if ttl <= 0 {
		return 0, 0
	}
	cutoff := time.Now().Add(-ttl)
	q.mu.Lock()
	defer q.mu.Unlock()
	for uid, bucket := range q.origins {
		for id, e := range bucket {
			if e.enqueuedAt.Before(cutoff) {
				delete(bucket, id)
				origins++
			}
		}
		if len(bucket) == 0 {
			delete(q.origins, uid)
		}
	}
	for uid, bucket := range q.results {
		for id, e := range bucket {
			if e.recordedAt.Before(cutoff) {
				delete(bucket, id)
				results++
			}
		}
		if len(bucket) == 0 {
			delete(q.results, uid)
		}
	}
	for uid, bucket := range q.issued {
		for id, e := range bucket {
			if e.issuedAt.Before(cutoff) {
				delete(bucket, id)
			}
		}
		if len(bucket) == 0 {
			delete(q.issued, uid)
		}
	}
	return origins, results
}
