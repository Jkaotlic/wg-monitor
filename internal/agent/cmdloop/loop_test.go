package cmdloop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fakeClient struct {
	mu       sync.Mutex
	pollSeq  []pollResp
	pollIdx  int
	pollHits int32
	posts    []wire.CommandResult
	postErr  error
}

type pollResp struct {
	cmd *wire.Command
	err error
}

func (f *fakeClient) PollCommand(ctx context.Context, waitSec int) (*wire.Command, error) {
	atomic.AddInt32(&f.pollHits, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pollIdx >= len(f.pollSeq) {
		// hold ctx until canceled to mimic long-poll wait
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return nil, ctx.Err()
	}
	r := f.pollSeq[f.pollIdx]
	f.pollIdx++
	return r.cmd, r.err
}

func (f *fakeClient) PostResult(ctx context.Context, r wire.CommandResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, r)
	return f.postErr
}

type fakeRunner struct {
	mu   sync.Mutex
	seen []wire.Command
	ret  func(wire.Command) wire.CommandResult
}

func (r *fakeRunner) Execute(ctx context.Context, c wire.Command) wire.CommandResult {
	r.mu.Lock()
	r.seen = append(r.seen, c)
	r.mu.Unlock()
	if r.ret != nil {
		return r.ret(c)
	}
	return wire.CommandResult{ID: c.ID, Status: "ok"}
}

func TestLoop_PollsExecutesPosts(t *testing.T) {
	cl := &fakeClient{
		pollSeq: []pollResp{
			{cmd: &wire.Command{ID: "c1", Action: "diag_now"}},
		},
	}
	rn := &fakeRunner{}
	l := New(cl, rn, 5)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	rn.mu.Lock()
	defer rn.mu.Unlock()
	if len(rn.seen) != 1 || rn.seen[0].ID != "c1" {
		t.Errorf("runner seen: %+v", rn.seen)
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if len(cl.posts) != 1 || cl.posts[0].ID != "c1" || cl.posts[0].Status != "ok" {
		t.Errorf("posts: %+v", cl.posts)
	}
}

func TestLoop_NilCommandSkipsPost(t *testing.T) {
	cl := &fakeClient{
		pollSeq: []pollResp{
			{cmd: nil}, // 204
			{cmd: &wire.Command{ID: "c2", Action: "pingcheck_now"}}, // then a real one
		},
	}
	rn := &fakeRunner{}
	l := New(cl, rn, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	rn.mu.Lock()
	defer rn.mu.Unlock()
	if len(rn.seen) != 1 || rn.seen[0].ID != "c2" {
		t.Errorf("runner should have seen exactly c2; got %+v", rn.seen)
	}
}

func TestLoop_PollErrorBackoffsThenRetries(t *testing.T) {
	cl := &fakeClient{
		pollSeq: []pollResp{
			{err: errors.New("transient")},
			{cmd: &wire.Command{ID: "c3", Action: "force_recheck"}},
		},
	}
	rn := &fakeRunner{}
	l := New(cl, rn, 1)
	// Tiny backoff so test is fast
	l.BackoffBase = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	if atomic.LoadInt32(&cl.pollHits) < 2 {
		t.Errorf("expected at least 2 poll attempts after error, got %d", cl.pollHits)
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	if len(rn.seen) == 0 || rn.seen[0].ID != "c3" {
		t.Errorf("loop did not recover from error; seen=%+v", rn.seen)
	}
}

func TestLoopBackoffUsesFullJitter(t *testing.T) {
	l := New(&fakeClient{}, &fakeRunner{}, 1)
	l.BackoffBase = time.Second
	l.BackoffMax = 60 * time.Second
	l.randFloat64 = func() float64 { return 0.5 }

	got := l.backoff(4)
	if got != 4*time.Second {
		t.Fatalf("backoff with jitter=%s, want 4s for attempt cap 8s at 0.5", got)
	}
}

func TestLoop_PostResultErrorContinues(t *testing.T) {
	cl := &fakeClient{
		pollSeq: []pollResp{
			{cmd: &wire.Command{ID: "c4", Action: "diag_now"}},
			{cmd: &wire.Command{ID: "c5", Action: "diag_now"}},
		},
		postErr: errors.New("backend transient"),
	}
	rn := &fakeRunner{}
	l := New(cl, rn, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	rn.mu.Lock()
	defer rn.mu.Unlock()
	if len(rn.seen) < 2 {
		t.Errorf("loop should keep running through post errors, seen=%v", rn.seen)
	}
}

func TestLoopDeduplicatesCommandIDAndRepostsCachedResult(t *testing.T) {
	cl := &fakeClient{
		pollSeq: []pollResp{
			{cmd: &wire.Command{ID: "same", Action: "route_add"}},
			{cmd: &wire.Command{ID: "same", Action: "route_add"}},
		},
	}
	rn := &fakeRunner{}
	l := New(cl, rn, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	l.Run(ctx)

	rn.mu.Lock()
	seen := len(rn.seen)
	rn.mu.Unlock()
	if seen != 1 {
		t.Fatalf("runner executions=%d, want 1", seen)
	}
	cl.mu.Lock()
	posts := len(cl.posts)
	cl.mu.Unlock()
	if posts != 2 {
		t.Fatalf("posts=%d, want cached result posted twice", posts)
	}
}

func TestLoop_CtxCancelExitsCleanly(t *testing.T) {
	cl := &fakeClient{} // empty seq → blocks on long-poll
	rn := &fakeRunner{}
	l := New(cl, rn, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not exit on ctx cancel")
	}
}
