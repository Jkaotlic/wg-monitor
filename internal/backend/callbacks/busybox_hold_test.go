package callbacks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	commandqueue "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestBusyboxHoldBot(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateDeployInfo("vasya", db.DeployInfo{LastDeployedVersion: "v0.66.3"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	q := commandqueue.New()
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.SetBusyboxHoldQueue(q)
	// Owner and unrelated sender cannot mutate router-global packages.
	_ = d.Users().SetTelegramUserID(uid, 100)
	r.HandleMessage(context.Background(), dm(100, "/busybox_hold all"))
	r.HandleMessage(context.Background(), &tg.Message{Chat: tg.Chat{ID: -100}, From: tg.User{ID: 42}, Text: "/busybox_hold all"})
	if q.HasActiveCommand(uid, "opkg_busybox_hold") {
		t.Fatal("unauthorized command")
	}
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold all"))
	c, ok := q.Dequeue(context.Background(), uid, 0)
	if !ok || c.Action != "opkg_busybox_hold" {
		t.Fatalf("command=%+v", c)
	}
	if c.Args != nil {
		t.Fatal("unexpected args")
	}
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold vasya"))
	if next, ok := q.Dequeue(context.Background(), uid, 0); ok {
		t.Fatalf("duplicate: %+v", next)
	}
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold status"))
	if !strings.Contains(f.sentMsgs[len(f.sentMsgs)-1], "ожидается") {
		t.Fatalf("pending: %v", f.sentMsgs)
	}
	if err := q.RecordResult(uid, wire.CommandResult{ID: c.ID, Status: "ok", Output: "BusyBox: hold подтверждён"}); err != nil {
		t.Fatal(err)
	}
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold status"))
	if !strings.Contains(f.sentMsgs[len(f.sentMsgs)-1], "hold подтверждён") {
		t.Fatal(f.sentMsgs)
	}
}

func TestBusyboxHoldOldAgentAndUnknownRouter(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	q := commandqueue.New()
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.SetBusyboxHoldQueue(q)
	for _, text := range []string{"/busybox_hold all", "/busybox_hold unknown"} {
		r.HandleMessage(context.Background(), dm(42, text))
	}
	if q.HasActiveCommand(uid, "opkg_busybox_hold") {
		t.Fatal("old agent got command")
	}
	if len(f.sentMsgs) != 2 || !strings.Contains(f.sentMsgs[0], "обновить агент") || !strings.Contains(f.sentMsgs[1], "не найден") {
		t.Fatal(f.sentMsgs)
	}
}

func TestBusyboxHoldUnconfirmedAfterExpiryOrQueueRestart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		restart bool
	}{{"expired", false}, {"queue restarted", true}} {
		t.Run(tc.name, func(t *testing.T) {
			d, uid := newTestDB(t)
			f := &fakeRouterTG{}
			q := commandqueue.New()
			r := NewRouter(d, f, Config{AdminUserID: 42})
			r.SetBusyboxHoldQueue(q)
			attempt := busyboxHoldAttempt{ID: "lost-hold", ExpiresAt: time.Now().Add(-time.Second)}
			if tc.restart {
				attempt.ExpiresAt = time.Now().Add(time.Minute)
			} else {
				if err := q.Enqueue(uid, wire.Command{ID: attempt.ID, Action: "opkg_busybox_hold", ExpiresAt: attempt.ExpiresAt}); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := json.Marshal(attempt)
			if err := d.KV().Set(fmt.Sprintf("busybox_hold:%d", uid), string(data)); err != nil {
				t.Fatal(err)
			}
			r.HandleMessage(context.Background(), dm(42, "/busybox_hold status"))
			if len(f.sentMsgs) != 1 || !strings.Contains(f.sentMsgs[0], "защита НЕ подтверждена") || strings.Contains(f.sentMsgs[0], "✅") {
				t.Fatal(f.sentMsgs)
			}
		})
	}
}

func TestBusyboxHoldMixedFleetAndFailedResult(t *testing.T) {
	d, uid := newTestDB(t)
	if err := d.Users().UpdateDeployInfo("vasya", db.DeployInfo{LastDeployedVersion: "v0.66.3"}); err != nil {
		t.Fatal(err)
	}
	old, err := d.Users().Insert("router-old", "old-token", "198.51.100.2", "nwg0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRouterTG{}
	q := commandqueue.New()
	r := NewRouter(d, f, Config{AdminUserID: 42})
	r.SetBusyboxHoldQueue(q)
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold all"))
	c, ok := q.Dequeue(context.Background(), uid, 0)
	if !ok {
		t.Fatal("no command")
	}
	if q.HasActiveCommand(old, "opkg_busybox_hold") {
		t.Fatal("old agent queued")
	}
	if len(f.sentMsgs) != 2 {
		t.Fatal(f.sentMsgs)
	}
	if err := q.RecordResult(uid, wire.CommandResult{ID: c.ID, Status: "locked", Output: "Обновление уже выполняется; повторите позже."}); err != nil {
		t.Fatal(err)
	}
	f.sentMsgs = nil
	r.HandleMessage(context.Background(), dm(42, "/busybox_hold status"))
	joined := strings.Join(f.sentMsgs, "\n")
	if strings.Contains(joined, "✅") || !strings.Contains(joined, "❌") || !strings.Contains(joined, "locked") {
		t.Fatal(joined)
	}
}
