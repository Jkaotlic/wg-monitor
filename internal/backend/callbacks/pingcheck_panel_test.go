package callbacks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakePingCheckTG struct {
	lastChatID int64
	lastMsgID  int64
	lastText   string
	lastKb     *tg.InlineKeyboardMarkup
	editErr    error
}

func (f *fakePingCheckTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.lastChatID = chatID
	f.lastMsgID = msgID
	f.lastText = text
	f.lastKb = kb
	return f.editErr
}

func TestPingCheckPanelNotifier_Status_OK(t *testing.T) {
	d, uid := newTestDB(t)
	user, err := d.Users().GetByID(uid)
	if err != nil || user == nil {
		t.Fatalf("GetByID(%d): %v", uid, err)
	}
	tgFake := &fakePingCheckTG{}
	n := &PingCheckPanelNotifier{TG: tgFake, DB: d}

	body := `{"enabled":true,"tunnels":[{"tunnelId":"awg10","tunnelName":"amst","ndmsName":"Wireguard0","enabled":true,"status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"tunnelRunning":true}]}`
	res := wire.CommandResult{Status: "ok", Output: body}
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_status"}

	if err := n.NotifyCommandResult(context.Background(), ref, res, user.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tgFake.lastChatID != 100 || tgFake.lastMsgID != 200 {
		t.Errorf("edit target wrong: %+v", tgFake)
	}
	for _, want := range []string{"📡 PingCheck", user.Nickname, "amst", "82ms", "🟢"} {
		if !strings.Contains(tgFake.lastText, want) {
			t.Errorf("missing %q in:\n%s", want, tgFake.lastText)
		}
	}

	// Verify toggle button cb_data has the resolved NDMSName (not empty).
	hasGoodNDMS := false
	for _, row := range tgFake.lastKb.InlineKeyboard {
		for _, b := range row {
			if strings.Contains(b.CallbackData, "pingcheck_toggle:") &&
				strings.Contains(b.CallbackData, ":Wireguard0:") {
				hasGoodNDMS = true
			}
		}
	}
	if !hasGoodNDMS {
		t.Errorf("toggle cb_data should include resolved NDMSName 'Wireguard0'; got kb=%+v", tgFake.lastKb)
	}
}

func TestPingCheckPanelNotifier_Status_AgentErr(t *testing.T) {
	d, uid := newTestDB(t)
	user, err := d.Users().GetByID(uid)
	if err != nil || user == nil {
		t.Fatalf("GetByID(%d): %v", uid, err)
	}
	tgFake := &fakePingCheckTG{}
	n := &PingCheckPanelNotifier{TG: tgFake, DB: d}

	res := wire.CommandResult{Status: "err", Output: "HTTP_REFUSED: dial tcp"}
	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_status"}

	if err := n.NotifyCommandResult(context.Background(), ref, res, user.ID); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(tgFake.lastText, "агент не ответил") {
		t.Errorf("expected err banner, got: %s", tgFake.lastText)
	}
	for _, want := range []string{"pingcheck_open", "router_doctor", "tunnels_refresh", "routes_open", "maint_open"} {
		if !keyboardHasCallback(tgFake.lastKb, want) {
			t.Fatalf("pingcheck status error keyboard missing %q: %#v", want, tgFake.lastKb)
		}
	}
}

type fakePingCheckEnqueuer struct {
	mu       sync.Mutex
	commands []wire.Command
	refs     []cmdpkg.MessageRef
	calls    int
	errOn    int
	err      error
}

func (f *fakePingCheckEnqueuer) Enqueue(userID int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.errOn > 0 && f.calls == f.errOn {
		return f.err
	}
	f.commands = append(f.commands, cmd)
	f.refs = append(f.refs, cmdpkg.MessageRef{})
	return nil
}
func (f *fakePingCheckEnqueuer) EnqueueWithRef(userID int64, cmd wire.Command, ref cmdpkg.MessageRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.errOn > 0 && f.calls == f.errOn {
		return f.err
	}
	f.commands = append(f.commands, cmd)
	f.refs = append(f.refs, ref)
	return nil
}

func TestPingCheckOpen_EnqueuesStatus(t *testing.T) {
	sink := &fakePingCheckEnqueuer{}
	a := NewPingCheckOpenAction(sink, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_open", UserID: 7, IsPanel: true}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(sink.commands) != 1 {
		t.Fatalf("expected 1 enqueue, got %d", len(sink.commands))
	}
	if sink.commands[0].Action != "pingcheck_status" {
		t.Errorf("got action %q", sink.commands[0].Action)
	}
	if sink.refs[0].Action != "pingcheck_status" {
		t.Errorf("ref must carry action for notifier dispatch")
	}
}

func TestPingCheckToggle_EnqueuesAndRefreshes(t *testing.T) {
	sink := &fakePingCheckEnqueuer{}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// One toggle + one auto-refresh
	if len(sink.commands) != 2 {
		t.Fatalf("expected 2 enqueues (toggle + status), got %d", len(sink.commands))
	}
	if sink.commands[0].Action != "pingcheck_toggle" {
		t.Errorf("first should be toggle, got %q", sink.commands[0].Action)
	}
	if sink.commands[1].Action != "pingcheck_status" {
		t.Errorf("second should be status auto-refresh, got %q", sink.commands[1].Action)
	}
	// Toggle args carry tunnel_id, ndms_name, enable
	got := sink.commands[0].Args
	if got["tunnel_id"] != "awg10" || got["ndms_name"] != "Wireguard0" || got["enable"] != false {
		t.Errorf("toggle args wrong: %+v", got)
	}
}

func TestPingCheckToggle_AutoRefreshFailureDoesNotFailQueuedToggle(t *testing.T) {
	sink := &fakePingCheckEnqueuer{errOn: 2, err: errors.New("queue full")}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}

	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("toggle already queued; auto-refresh failure should not fail Apply: %v", err)
	}
	if len(sink.commands) != 1 || sink.commands[0].Action != "pingcheck_toggle" {
		t.Fatalf("expected only queued toggle after refresh enqueue fails, commands=%+v", sink.commands)
	}
}

func TestPingCheckToggle_PrimaryEnqueueFailureReleasesInflight(t *testing.T) {
	sink := &fakePingCheckEnqueuer{errOn: 1, err: errors.New("queue full")}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}

	if _, err := a.Apply(context.Background(), q, args); err == nil {
		t.Fatalf("first toggle enqueue should fail")
	}
	sink.errOn = 0
	if _, err := a.Apply(context.Background(), q, args); err != nil {
		t.Fatalf("retry after primary enqueue failure should not be blocked by stale inflight: %v", err)
	}
	if len(sink.commands) != 2 || sink.commands[0].Action != "pingcheck_toggle" || sink.commands[1].Action != "pingcheck_status" {
		t.Fatalf("retry should enqueue toggle and refresh, commands=%+v", sink.commands)
	}
}

func TestPingCheckToggle_DupTapBlocked(t *testing.T) {
	sink := &fakePingCheckEnqueuer{}
	store := newPingCheckInflightStore()
	a := NewPingCheckToggleAction(sink, store, defaultCmdID)
	q := &tg.CallbackQuery{ID: "qid", Message: tg.Message{Chat: tg.Chat{ID: 100}, MessageID: 200}}
	args := Args{Action: "pingcheck_toggle", UserID: 7, PingCheckTunnelID: "awg10", NDMSName: "Wireguard0", PingCheckEnable: false}
	_, _ = a.Apply(context.Background(), q, args)
	// Immediate second tap → dup
	_, err := a.Apply(context.Background(), q, args)
	if err == nil || !strings.Contains(err.Error(), "уже выполняется") {
		t.Fatalf("expected dup err, got %v", err)
	}
	// First tap = 2 enqueues; second tap should add nothing.
	if len(sink.commands) != 2 {
		t.Errorf("dup must not enqueue; got %d total", len(sink.commands))
	}
}

func TestPingCheckInflightStore_TTLEvicts(t *testing.T) {
	s := newPingCheckInflightStore()
	if !s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("first claim must succeed")
	}
	if s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("second claim within TTL must fail")
	}
	time.Sleep(20 * time.Millisecond)
	if !s.tryClaim(7, "awg10", 10*time.Millisecond) {
		t.Fatal("after TTL the slot must be free again")
	}
}
