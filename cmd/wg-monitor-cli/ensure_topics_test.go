package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type fakeTopicCreator struct {
	mu     sync.Mutex
	calls  []fakeTopicCall
	nextID int64
	// errOn maps 1-based call ordinal → error to return; absence → success.
	errOn map[int]error
}

type fakeTopicCall struct {
	ChatID    int64
	Name      string
	IconColor int
}

func (f *fakeTopicCreator) CreateForumTopic(_ context.Context, chatID int64, name string, iconColor int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeTopicCall{chatID, name, iconColor})
	if err, ok := f.errOn[len(f.calls)]; ok {
		return 0, err
	}
	f.nextID++
	return 5000 + f.nextID, nil
}

func newEnsureTestDB(t *testing.T, nicknames ...string) (string, []int64) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ensure.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(nicknames))
	for i, n := range nicknames {
		uid, err := d.Users().Insert(n, n+"-tok", "1.1.1.1", "nwg0")
		if err != nil {
			t.Fatalf("insert %s: %v", n, err)
		}
		ids = append(ids, uid)
		_ = i
	}
	d.Close()
	return dbPath, ids
}

// TestEnsureTopics_CreatesForUsersMissingTopic: bulk path. Two users
// without topics → both get topics; pre-bound user is skipped.
func TestEnsureTopics_CreatesForUsersMissingTopic(t *testing.T) {
	dbPath, ids := newEnsureTestDB(t, "alpha", "beta", "gamma")
	d, _ := db.Open(dbPath)
	if err := d.Users().UpdateThreadID(ids[1], 999); err != nil {
		t.Fatal(err)
	}
	d.Close()

	fc := &fakeTopicCreator{}
	var out bytes.Buffer
	err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:  dbPath,
		ChatID:  -100,
		Creator: fc,
		Out:     &out,
	})
	if err != nil {
		t.Fatalf("run: %v\noutput:\n%s", err, out.String())
	}
	if len(fc.calls) != 2 {
		t.Fatalf("want 2 CreateForumTopic calls (alpha, gamma), got %d", len(fc.calls))
	}
	if fc.calls[0].Name != "👤 alpha" || fc.calls[1].Name != "👤 gamma" {
		t.Errorf("names: %+v", fc.calls)
	}
	for _, c := range fc.calls {
		if c.ChatID != -100 {
			t.Errorf("chat id: %d", c.ChatID)
		}
		if c.IconColor != 0xFF8C00 {
			t.Errorf("icon color: %d", c.IconColor)
		}
	}
	// Persistence: alpha and gamma now have thread ids; beta untouched.
	d2, _ := db.Open(dbPath)
	defer d2.Close()
	uA, _ := d2.Users().GetByNickname("alpha")
	uB, _ := d2.Users().GetByNickname("beta")
	uG, _ := d2.Users().GetByNickname("gamma")
	if uA.TelegramThreadID == nil || *uA.TelegramThreadID != 5001 {
		t.Errorf("alpha thread: %+v", uA.TelegramThreadID)
	}
	if uB.TelegramThreadID == nil || *uB.TelegramThreadID != 999 {
		t.Errorf("beta thread (must be untouched): %+v", uB.TelegramThreadID)
	}
	if uG.TelegramThreadID == nil || *uG.TelegramThreadID != 5002 {
		t.Errorf("gamma thread: %+v", uG.TelegramThreadID)
	}
	got := out.String()
	for _, want := range []string{"+ created alpha", "= skip beta", "+ created gamma", "Done: 2 created, 1 skipped, 0 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q in:\n%s", want, got)
		}
	}
}

// TestEnsureTopics_SingleNickname: --nickname=X path targets one user.
func TestEnsureTopics_SingleNickname(t *testing.T) {
	dbPath, _ := newEnsureTestDB(t, "alpha", "beta")
	fc := &fakeTopicCreator{}
	var out bytes.Buffer
	if err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:   dbPath,
		ChatID:   -100,
		Nickname: "beta",
		Creator:  fc,
		Out:      &out,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fc.calls) != 1 {
		t.Fatalf("want 1 call (beta only), got %d", len(fc.calls))
	}
	if fc.calls[0].Name != "👤 beta" {
		t.Errorf("name: %s", fc.calls[0].Name)
	}
}

// TestEnsureTopics_PartialFailureKeepsGoing: TG error on user 2/3 must
// not abort processing of user 3/3. Run returns an error overall but the
// non-failed users got their topics.
func TestEnsureTopics_PartialFailureKeepsGoing(t *testing.T) {
	dbPath, _ := newEnsureTestDB(t, "alpha", "beta", "gamma")
	fc := &fakeTopicCreator{errOn: map[int]error{2: errors.New("flood wait 60")}}
	var out bytes.Buffer
	err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:  dbPath,
		ChatID:  -100,
		Creator: fc,
		Out:     &out,
	})
	if err == nil {
		t.Fatal("want overall error due to partial failure")
	}
	if !strings.Contains(err.Error(), "1 user(s) failed") {
		t.Errorf("error message: %v", err)
	}
	if len(fc.calls) != 3 {
		t.Fatalf("want 3 attempts despite mid-fail, got %d", len(fc.calls))
	}
	d, _ := db.Open(dbPath)
	defer d.Close()
	uA, _ := d.Users().GetByNickname("alpha")
	uB, _ := d.Users().GetByNickname("beta")
	uG, _ := d.Users().GetByNickname("gamma")
	if uA.TelegramThreadID == nil {
		t.Error("alpha should have topic")
	}
	if uB.TelegramThreadID != nil {
		t.Errorf("beta should have NO topic after fail, got %v", *uB.TelegramThreadID)
	}
	if uG.TelegramThreadID == nil {
		t.Error("gamma should have topic")
	}
	if !strings.Contains(out.String(), "! fail beta") {
		t.Errorf("output should mention beta failure:\n%s", out.String())
	}
}

// TestEnsureTopics_RejectsZeroChat: 0 chat_id is unconfigured — refuse
// before issuing any TG call.
func TestEnsureTopics_RejectsZeroChat(t *testing.T) {
	dbPath, _ := newEnsureTestDB(t, "alpha")
	fc := &fakeTopicCreator{}
	err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:  dbPath,
		ChatID:  0,
		Creator: fc,
		Out:     &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "chat_id is required") {
		t.Fatalf("want chat_id error, got %v", err)
	}
	if len(fc.calls) != 0 {
		t.Errorf("no calls expected, got %d", len(fc.calls))
	}
}

// TestEnsureTopics_ForceRebuilds: --force should rebuild a topic even
// when telegram_thread_id is already set; the old id is overwritten in
// the DB and the user's row now points at the new topic.
func TestEnsureTopics_ForceRebuilds(t *testing.T) {
	dbPath, ids := newEnsureTestDB(t, "delta")
	d, _ := db.Open(dbPath)
	if err := d.Users().UpdateThreadID(ids[0], 111); err != nil {
		t.Fatal(err)
	}
	d.Close()

	fc := &fakeTopicCreator{}
	var out bytes.Buffer
	if err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:  dbPath,
		ChatID:  -100,
		Force:   true,
		Creator: fc,
		Out:     &out,
	}); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if len(fc.calls) != 1 {
		t.Fatalf("want 1 call (force rebuild), got %d", len(fc.calls))
	}
	d2, _ := db.Open(dbPath)
	defer d2.Close()
	u, _ := d2.Users().GetByNickname("delta")
	if u.TelegramThreadID == nil || *u.TelegramThreadID == 111 {
		t.Fatalf("force did not rebuild; thread id still %v", u.TelegramThreadID)
	}
	if !strings.Contains(out.String(), "rebuilt delta — old topic id=111") {
		t.Errorf("output should announce rebuild:\n%s", out.String())
	}
}

// TestEnsureTopics_NoSleepInTests: sleep defaults to 0 in tests (caller
// passes 0 → no time.After branch), verifying calls happen back-to-back
// without scheduling overhead.
func TestEnsureTopics_NoSleepBetween(t *testing.T) {
	dbPath, _ := newEnsureTestDB(t, "u1", "u2", "u3")
	fc := &fakeTopicCreator{}
	if err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath: dbPath, ChatID: -100, Creator: fc, Out: &bytes.Buffer{}, Sleep: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fc.calls) != 3 {
		t.Fatalf("calls: %d", len(fc.calls))
	}
}
