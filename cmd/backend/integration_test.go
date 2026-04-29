package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/callbacks"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/realert"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// TestStage2EndToEnd exercises: HARD with keyboard → callback Silence → edited message → state row updated.
// Then ages the row 7h, ensures realert poller picks up and sends a STILL-DOWN reminder.
func TestStage2EndToEnd(t *testing.T) {
	// 1. Spin up fake TG server that records calls
	var mu sync.Mutex
	sentMsgs := []map[string]any{}
	edits := []map[string]any{}
	answers := []map[string]any{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			sentMsgs = append(sentMsgs, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1000 + len(sentMsgs)}})
		case strings.HasSuffix(r.URL.Path, "/createForumTopic"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_thread_id": 7}})
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			edits = append(edits, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			answers = append(answers, req)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{}})
		}
	}))
	defer srv.Close()

	// 2. Set up DB + user
	tmp := t.TempDir() + "/test.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, err := d.Users().Insert("vasya", "rawtoken", "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}

	// 3. Build TG client + dispatcher + callbacks router + realert poller.
	// BaseURL + Token + "/" + method → srv.URL + "/bot" + "t" + "/sendMessage"
	tgC := &tg.Client{BaseURL: srv.URL + "/bot", Token: "t", HTTP: srv.Client()}
	disp := alerts.NewDispatcher(d, tgC, alerts.Config{
		ChatID:            -100,
		FailThreshold:     3,
		RecoveryThreshold: 2,
	})
	router := callbacks.NewRouter(d, tgC, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})
	poller := realert.NewPoller(d, tgC, realert.Config{
		ChatID: -100, RealertEvery: 6 * time.Hour, TickEvery: time.Second,
	})

	// 4. Trigger 3 fails → HARD.
	// The FSM reads persisted state each iteration so ConsecutiveFails accumulates.
	ctx := context.Background()
	th := state.Thresholds{Fail: 3, Recovery: 2}
	for i := 0; i < 3; i++ {
		prev, _ := d.State().Get(uid, "awg_handshake")
		tr := state.Apply(prev, "fail", time.Now(), th)
		if err := disp.Handle(ctx, uid, "vasya", "awg_handshake", tr, wire.Check{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "h=200s"}}); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	if len(sentMsgs) != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 HARD message, got %d", len(sentMsgs))
	}
	if sentMsgs[0]["reply_markup"] == nil {
		t.Errorf("HARD message missing keyboard")
	}
	hardText := sentMsgs[0]["text"].(string)
	mu.Unlock()

	// 5. Simulate callback Silence(1h).
	q := &tg.CallbackQuery{
		ID:   "cbk-1",
		From: tg.User{ID: 555},
		Message: tg.Message{
			MessageID: 1001, Chat: tg.Chat{ID: -100},
			Text: hardText,
		},
		Data: fmt.Sprintf("silence:%d:awg_handshake:1h", uid),
	}
	router.HandleCallback(ctx, q)

	mu.Lock()
	if len(answers) != 1 {
		t.Errorf("expected 1 answerCallback, got %d", len(answers))
	}
	if len(edits) != 1 {
		t.Errorf("expected 1 edit, got %d", len(edits))
	} else if !strings.Contains(edits[0]["text"].(string), "Silenced") {
		t.Errorf("edit missing 'Silenced': %v", edits[0]["text"])
	}
	mu.Unlock()

	// 6. Verify state has SilencedUntil set.
	st, err := d.State().Get(uid, "awg_handshake")
	if err != nil {
		t.Fatal(err)
	}
	if st.SilencedUntil == nil {
		t.Error("SilencedUntil nil after silence callback")
	}

	// 7. Age the state: clear silence, set hard_since/last_alert_at to 7h ago.
	aged := time.Now().Add(-7 * time.Hour)
	st.SilencedUntil = nil
	st.HardSince = &aged
	st.LastAlertAt = &aged
	if err := d.State().Save(uid, "awg_handshake", st); err != nil {
		t.Fatal(err)
	}

	// 8. Trigger realert tick — Run with short timeout to fire 1 tick.
	mu.Lock()
	initialMsgs := len(sentMsgs)
	mu.Unlock()
	runCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	_ = poller.Run(runCtx)
	cancel()
	poller.WaitForExit()

	mu.Lock()
	realertSent := len(sentMsgs) - initialMsgs
	if realertSent != 1 {
		mu.Unlock()
		t.Fatalf("expected 1 realert message, got %d (total %d)", realertSent, len(sentMsgs))
	}
	last := sentMsgs[len(sentMsgs)-1]
	if !strings.Contains(last["text"].(string), "STILL DOWN") {
		t.Errorf("realert text missing 'STILL DOWN': %v", last["text"])
	}
	if last["reply_markup"] != nil {
		t.Errorf("realert must not have keyboard")
	}
	mu.Unlock()
}
