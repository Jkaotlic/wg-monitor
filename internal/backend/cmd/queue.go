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
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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
}

// Queue is per-user FIFO queues plus a per-(user,id) result map plus a
// per-(user,id) origin map. Concurrent-safe. Single mutex is fine —
// operations are short and the fleet is ~10 users, not 10k.
type Queue struct {
	mu      sync.Mutex
	pending map[int64][]wire.Command // userID → FIFO
	results map[int64]map[string]wire.CommandResult
	// origins maps (userID → cmd.ID → MessageRef). Populated by
	// EnqueueWithRef; consumed by the cmd-result handler to relay TG replies.
	origins map[int64]map[string]MessageRef
	signal  *sync.Cond // signals on Enqueue and RecordResult
}

func New() *Queue {
	q := &Queue{
		pending: make(map[int64][]wire.Command),
		results: make(map[int64]map[string]wire.CommandResult),
		origins: make(map[int64]map[string]MessageRef),
	}
	q.signal = sync.NewCond(&q.mu)
	return q
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
		bucket = make(map[string]MessageRef)
		q.origins[userID] = bucket
	}
	bucket[cmd.ID] = ref
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
	return r, ok
}

// ConsumeOriginRef is OriginRef + delete in one shot. Use from the result
// handler so the same ref isn't relayed twice if RecordResult somehow fires
// twice for the same command id.
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
	return r, ok
}

// Enqueue appends cmd to userID's queue and wakes any waiter.
// Validates ID non-empty and Action whitelist.
func (q *Queue) Enqueue(userID int64, cmd wire.Command) error {
	if cmd.ID == "" {
		return errors.New("command id is required")
	}
	if !wire.IsValidCommandAction(cmd.Action) {
		return errors.New("invalid command action: " + cmd.Action)
	}
	q.mu.Lock()
	q.pending[userID] = append(q.pending[userID], cmd)
	q.mu.Unlock()
	q.signal.Broadcast()
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
	// so the cond.Wait below can unblock without a busy-loop.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(holdTimeout):
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
			q.pending[userID] = cmds[1:]
			return &head, true
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, false
		}
		q.signal.Wait()
	}
}

// RecordResult stores result under (userID, result.ID) and wakes any AwaitResult waiter.
func (q *Queue) RecordResult(userID int64, result wire.CommandResult) error {
	if result.ID == "" {
		return errors.New("result id is required")
	}
	if !wire.IsValidCommandResultStatus(result.Status) {
		return errors.New("invalid result status: " + result.Status)
	}
	q.mu.Lock()
	bucket, ok := q.results[userID]
	if !ok {
		bucket = make(map[string]wire.CommandResult)
		q.results[userID] = bucket
	}
	bucket[result.ID] = result
	q.mu.Unlock()
	q.signal.Broadcast()
	return nil
}

// AwaitResult blocks until RecordResult lands a matching (userID,id) entry,
// or the timeout/ctx-cancel hits. Useful for the TG callback handler that
// wants to display the action's outcome inline.
func (q *Queue) AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool) {
	deadline := time.Now().Add(timeout)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(timeout):
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
				return &r, true
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil, false
		}
		q.signal.Wait()
	}
}
