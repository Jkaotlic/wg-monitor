// cmd/backend/backend_pingcheck_integration_test.go
//
// Integration test: pingcheck_open callback → agent polls pingcheck_status
// command → runner calls fake awg-manager /api/pingcheck/status → posts result
// → backend dispatches to PingCheckPanelNotifier → EditMessageText on the
// panel message.
//
// Mirrors TestIntegration_DiagNow_NoReportAutoTriggers in harness structure.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/agent"
	"github.com/anex/wg-monitor/internal/agent/actions"
	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/backend"
	"github.com/anex/wg-monitor/internal/backend/callbacks"
	bcmd "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

// capturingEditTG extends noopTG by recording EditMessageText calls.
type capturingEditTG struct {
	noopTG
	mu    sync.Mutex
	edits []tgEditCall
}

type tgEditCall struct {
	chatID    int64
	messageID int64
	text      string
	kb        *tg.InlineKeyboardMarkup
}

func (c *capturingEditTG) EditMessageText(_ context.Context, chatID, msgID int64, text, _ string, kb *tg.InlineKeyboardMarkup) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.edits = append(c.edits, tgEditCall{chatID: chatID, messageID: msgID, text: text, kb: kb})
	return nil
}

func (c *capturingEditTG) editCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.edits)
}

func (c *capturingEditTG) lastEdit() (tgEditCall, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.edits) == 0 {
		return tgEditCall{}, false
	}
	return c.edits[len(c.edits)-1], true
}

// cannedPingCheckStatus is the JSON envelope the fake awg-manager returns for
// GET /api/pingcheck/status. One tunnel named "amst" that is alive at 15 ms.
var cannedPingCheckStatus = `{
	"success": true,
	"data": {
		"enabled": true,
		"tunnels": [
			{
				"tunnelId":      "awg10",
				"tunnelName":    "amst",
				"enabled":       true,
				"status":        "alive",
				"lastLatency":   15,
				"failCount":     0,
				"successCount":  42,
				"failThreshold": 3,
				"restartCount":  0,
				"tunnelRunning": true
			}
		]
	}
}`

// cannedTunnelsAll is the JSON envelope the fake awg-manager returns for
// GET /api/tunnels/all. Provides the ndmsName mapping used by PingCheckStatusJSON.
var cannedTunnelsAll = `{
	"success": true,
	"data": {
		"external": [],
		"system":   [],
		"tunnels": [
			{
				"id":       "awg10",
				"ndmsName": "Wireguard0",
				"name":     "amst"
			}
		]
	}
}`

// TestIntegration_PingCheckPanel_OpenRoundtrip verifies the complete flow:
//
//  1. Router has SetPingCheck wired so pingcheck_open enqueues a pingcheck_status cmd.
//  2. HandleCallback("pingcheck_open:<uid>:_panel_") enqueues the command.
//  3. Agent polls the command, runs it against a fake awg-manager.
//  4. Agent POSTs the result back to the backend.
//  5. Backend's PingCheckNotifier calls EditMessageText with the rendered panel.
//  6. Rendered panel text contains "📡 PingCheck", tunnel name "amst", latency "15ms",
//     and the alive icon "🟢".
func TestIntegration_PingCheckPanel_OpenRoundtrip(t *testing.T) {
	// --- 1. Fake awg-manager ---
	var pingCheckHits int
	var awgMu sync.Mutex

	awgFake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		awgMu.Lock()
		defer awgMu.Unlock()
		switch r.URL.Path {
		case "/api/pingcheck/status":
			pingCheckHits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, cannedPingCheckStatus)
		case "/api/tunnels/all":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, cannedTunnelsAll)
		default:
			t.Errorf("unexpected awg-manager path: %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer awgFake.Close()

	// --- 2. DB + user ---
	tmp := t.TempDir() + "/pingcheck_roundtrip.db"
	d, err := db.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const tok = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	uid, err := d.Users().Insert("alice", tok, "1.2.3.4", "nwg0")
	if err != nil {
		t.Fatal(err)
	}

	// --- 3. Command queue + backend HTTP server ---
	queue := bcmd.New()

	// capturingEditTG is used both as the router's TG client AND as the
	// PingCheckPanelNotifier's TG client — the notifier calls EditMessageText
	// on the panel message which we capture here.
	capTG := &capturingEditTG{}

	// PingCheckPanelNotifier: the production notifier wired against capTG.
	pcNotifier := &callbacks.PingCheckPanelNotifier{TG: capTG, DB: d}

	mux := backend.NewMux(backend.Deps{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:                d,
		Dispatcher:        noopDispatcher{},
		CommandSink:       queue,
		PingCheckNotifier: pcNotifier,
		Thresholds:        state.Thresholds{Fail: 3, Recovery: 2},
	})
	backendSrv := httptest.NewServer(mux)
	defer backendSrv.Close()

	// --- 4. Router (uses noopTG for answer/edit side-effects we don't care about) ---
	router := callbacks.NewRouterWithSink(d, noopTG{}, queue, callbacks.Config{
		ChatID: -100, AdminUserID: 555, MuteCutoffHour: 9,
	})
	// Wire the PingCheck panel actions so pingcheck_open is handled.
	router.SetPingCheck(queue)

	// --- 5. Dispatch pingcheck_open callback ---
	// Message ID 7777 stands for the pre-existing "loading…" panel message.
	tid := int64(33)
	q := &tg.CallbackQuery{
		ID:   "cbk-pingcheck-open",
		From: tg.User{ID: 555},
		Message: tg.Message{
			MessageID: 7777, Chat: tg.Chat{ID: -100}, MessageThreadID: &tid,
			Text: "📡 PingCheck — alice\n\nЗагружаю состояние…",
		},
		Data: fmt.Sprintf("pingcheck_open:%d:_panel_", uid),
	}
	router.HandleCallback(context.Background(), q)

	// --- 6. Agent polls command + runs it ---
	agentClient := agent.NewClient(backendSrv.URL, tok, "test-agent-pc", 5*time.Second)
	cmd, err := agentClient.PollCommand(context.Background(), 2)
	if err != nil || cmd == nil {
		t.Fatalf("PollCommand: %v cmd=%+v", err, cmd)
	}
	if cmd.Action != "pingcheck_status" {
		t.Fatalf("expected pingcheck_status command, got %q", cmd.Action)
	}

	runner := &actions.Runner{AwgClient: awgmgr.New(awgFake.URL)}
	res := runner.Execute(context.Background(), *cmd)
	if res.Status != "ok" {
		t.Fatalf("runner.Execute: status=%q output=%q", res.Status, res.Output)
	}

	// Verify the runner actually called the fake awg-manager.
	awgMu.Lock()
	hits := pingCheckHits
	awgMu.Unlock()
	if hits != 1 {
		t.Errorf("expected 1 /api/pingcheck/status hit, got %d", hits)
	}

	// --- 7. Agent posts result back ---
	if err := agentClient.PostResult(context.Background(), res); err != nil {
		t.Fatalf("PostResult: %v", err)
	}

	// --- 8. Wait for the async relay (PingCheckNotifier.NotifyCommandResult →
	//         EditMessageText) to fire. ---
	if got := waitForRelay(t, capTG.editCount, 1, 3*time.Second); got < 1 {
		t.Fatalf("expected at least 1 EditMessageText call from PingCheckNotifier, got %d", got)
	}

	edit, ok := capTG.lastEdit()
	if !ok {
		t.Fatal("no edit captured")
	}

	// --- 9. Assert routing ---
	if edit.chatID != -100 || edit.messageID != 7777 {
		t.Errorf("edit routed to wrong message: chatID=%d messageID=%d (want -100, 7777)",
			edit.chatID, edit.messageID)
	}

	// --- 10. Assert rendered panel content ---
	text := edit.text
	wantFragments := []string{"📡 PingCheck", "alice", "amst", "15ms", "🟢"}
	for _, frag := range wantFragments {
		if !strings.Contains(text, frag) {
			t.Errorf("rendered panel missing %q\nfull text:\n%s", frag, text)
		}
	}

	// --- 11. Assert inline keyboard was built ---
	if edit.kb == nil {
		t.Error("EditMessageText called with nil keyboard; expected PingCheckPanelKeyboard")
	} else if len(edit.kb.InlineKeyboard) == 0 {
		t.Error("inline keyboard has no rows")
	} else {
		// At minimum the last two rows are: [Проверить сейчас / Обновить] and [Помощь / Закрыть].
		// At least one row should contain a button for the "amst" tunnel.
		foundTunnel := false
		for _, row := range edit.kb.InlineKeyboard {
			for _, btn := range row {
				if strings.Contains(btn.CallbackData, "awg10") {
					foundTunnel = true
				}
			}
		}
		if !foundTunnel {
			b, _ := json.Marshal(edit.kb)
			t.Errorf("keyboard missing toggle button for tunnel awg10; keyboard: %s", string(b))
		}
	}
}
