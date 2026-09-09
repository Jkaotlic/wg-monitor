package alerts

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// blockingTopicTG is a TGSender fake whose CreateForumTopic can be gated so
// tests can force two concurrent topic-creation attempts for the same user
// to overlap (C5). Every call returns a distinct, incrementing topic id —
// mimicking real Telegram, which mints a fresh thread per CreateForumTopic
// call — so a locking bug surfaces as callCount > 1 / mismatched returned
// ids rather than being masked by an idempotent fake.
type blockingTopicTG struct {
	mu           sync.Mutex
	callCount    int
	active       int
	maxActive    int
	nextID       int64
	blockClaimed bool // true once some call has claimed the "first call" gate below

	blockFirst   chan struct{} // if non-nil, the first call blocks here until closed
	firstEntered chan struct{} // if non-nil, closed once the first call starts blocking
}

func (f *blockingTopicTG) CreateForumTopic(_ context.Context, _ int64, _ string, _ int) (int64, error) {
	f.mu.Lock()
	f.callCount++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if f.nextID == 0 {
		f.nextID = 500
	}
	id := f.nextID
	f.nextID++
	// Claim the "first call" gate under the SAME lock as the counters above,
	// via a plain bool — NOT sync.Once. Once.Do blocks every concurrent
	// caller until the first caller's wrapped function returns, which would
	// deadlock any test that (like this fake is designed for) expects only
	// the true first caller to block while later callers return immediately.
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
	f.active--
	f.mu.Unlock()
	return id, nil
}

func (f *blockingTopicTG) SendMessage(context.Context, int64, *int64, string, string, *int64) (int64, error) {
	return 1, nil
}
func (f *blockingTopicTG) SendMessageWithKeyboard(context.Context, int64, *int64, string, string, *int64, *tg.InlineKeyboardMarkup) (int64, error) {
	return 1, nil
}
func (f *blockingTopicTG) SendMessageWithReplyKeyboard(context.Context, int64, *int64, string, string, *int64, any) (int64, error) {
	return 1, nil
}

var _ TGSender = (*blockingTopicTG)(nil)

// Одновременное создание темы для ОДНОГО роутера из двух админских команд
// (/ensure_topics и /recreate_topic) не имеет права завести две темы.
// EnsureTopicForUser сама по себе не потокобезопасна: без общего замка обе
// увидят «темы ещё нет», обе позовут CreateForumTopic, и чья запись в базу
// ляжет последней -- та и победит, осиротив вторую тему в Telegram.
//
// После переезда уведомлений в личку диспетчер тем больше не создаёт, но
// админские команды остались, и замок стережёт именно их.
func TestEnsureTopic_SharedLockPreventsDuplicateAcrossPaths(t *testing.T) {
	d := newDB(t)
	tok := "1111111111111111111111111111111111111111111111111111111111111111"
	uid, err := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}

	tgFake := &blockingTopicTG{
		blockFirst:   make(chan struct{}),
		firstEntered: make(chan struct{}),
	}
	type result struct {
		ref TopicRef
		err error
	}

	// Первая команда: её CreateForumTopic блокируется, изображая медленный
	// поход в Telegram, пока её не отпустят ниже.
	aDone := make(chan result, 1)
	go func() {
		unlock := LockTopicCreation(uid)
		defer unlock()
		ref, err := EnsureTopicForUser(context.Background(), tgFake, d, -100, uid, false)
		aDone <- result{ref, err}
	}()
	<-tgFake.firstEntered // A is now blocked inside CreateForumTopic, lock held

	// Вторая команда идёт тем же путём и обязана ждать замок.
	bDone := make(chan result, 1)
	go func() {
		unlock := LockTopicCreation(uid)
		defer unlock()
		ref, err := EnsureTopicForUser(context.Background(), tgFake, d, -100, uid, false)
		bDone <- result{ref, err}
	}()

	// B's own CreateForumTopic call (if it reached one) would NOT block —
	// blockOnce already fired for A. So if B completes within this window,
	// the lock is not actually shared across the two paths.
	select {
	case r := <-bDone:
		t.Fatalf("goroutine B returned before A released the lock (ref=%+v err=%v); topic-creation lock is not shared across paths", r.ref, r.err)
	case <-time.After(100 * time.Millisecond):
		// expected: B is still blocked waiting for the shared per-user lock.
	}

	close(tgFake.blockFirst)
	aRes := <-aDone
	if aRes.err != nil {
		t.Fatalf("первая команда (EnsureTopicForUser): %v", aRes.err)
	}
	bRes := <-bDone
	if bRes.err != nil {
		t.Fatalf("вторая команда (EnsureTopicForUser): %v", bRes.err)
	}

	tgFake.mu.Lock()
	callCount, maxActive := tgFake.callCount, tgFake.maxActive
	tgFake.mu.Unlock()
	if maxActive > 1 {
		t.Fatalf("CreateForumTopic re-entered concurrently: maxActive=%d, want <=1", maxActive)
	}
	if callCount != 1 {
		t.Fatalf("CreateForumTopic called %d times for one user; want exactly 1 (shared lock must prevent duplicate creation)", callCount)
	}
	if aRes.ref.ThreadID != bRes.ref.ThreadID {
		t.Fatalf("goroutines A and B got different topic ids: a=%d b=%d — duplicate/orphaned topic", aRes.ref.ThreadID, bRes.ref.ThreadID)
	}
}
