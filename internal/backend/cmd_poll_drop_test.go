package backend

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// A2.9: агент оборвал длинный опрос -- выданная в оборванный ответ
// self_update не должна держать слот раздачи 12 минут: команда возвращается
// в очередь, место в раздаче свободно сразу.
type pollDropEnv struct {
	q          *cmdpkg.Queue
	h          http.Handler
	tokA, tokB string
	uidA       int64
}

func newPollDropEnv(t *testing.T) *pollDropEnv {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "drop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	tokA := "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	tokB := "b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2"
	uidA, err := d.Users().InsertWithKind("router-a", tokA, "198.51.100.11", "awg0", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	uidB, err := d.Users().InsertWithKind("router-b", tokB, "198.51.100.12", "awg0", db.KindMobile)
	if err != nil {
		t.Fatal(err)
	}
	q := cmdpkg.New()
	q.SetDispatchLimit("self_update", 1, 12*time.Minute)
	for i, uid := range []int64{uidA, uidB} {
		if err := q.Enqueue(uid, wire.Command{ID: "su-" + string(rune('a'+i)), Action: "self_update"}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewMux(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, CommandSink: q})
	return &pollDropEnv{q: q, h: h, tokA: tokA, tokB: tokB, uidA: uidA}
}

func (e *pollDropEnv) pollB(t *testing.T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/cmd?wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+e.tokB)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("второй роутер получил %d: слот раздачи держит оборванный опрос", w.Code)
	}
}

func (e *pollDropEnv) requireBackInQueue(t *testing.T) {
	t.Helper()
	c, ok := e.q.Dequeue(context.Background(), e.uidA, time.Millisecond)
	if ok {
		t.Fatalf("после освобождения слота первый роутер сразу получил %q -- слот занят вторым, так быть не должно", c.ID)
	}
	if n := len(e.q.DropPending(e.uidA, "self_update")); n != 1 {
		t.Fatalf("недоставленная self_update потеряна: в очереди %d", n)
	}
}

func TestCmdPoll_ClientGoneBeforeDispatchKeepsSlotFree(t *testing.T) {
	e := newPollDropEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // агент уже оборвал соединение
	req := httptest.NewRequest(http.MethodGet, "/v1/cmd?wait=0", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+e.tokA)
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	e.pollB(t)
	e.requireBackInQueue(t)
}

// failWriter -- соединение умерло: запись ответа не проходит.
type failWriter struct{ h http.Header }

func (f *failWriter) Header() http.Header {
	if f.h == nil {
		f.h = http.Header{}
	}
	return f.h
}
func (f *failWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (f *failWriter) WriteHeader(int)           {}

func TestCmdPoll_ResponseWriteFailsReturnsCommandAndSlot(t *testing.T) {
	e := newPollDropEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/cmd?wait=0", nil)
	req.Header.Set("Authorization", "Bearer "+e.tokA)
	e.h.ServeHTTP(&failWriter{}, req)
	e.pollB(t)
	e.requireBackInQueue(t)
}
