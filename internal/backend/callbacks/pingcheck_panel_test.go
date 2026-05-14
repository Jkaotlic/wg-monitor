package callbacks

import (
	"context"
	"strings"
	"testing"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

	body := `{"enabled":true,"tunnels":[{"tunnelId":"awg10","tunnelName":"amst","enabled":true,"status":"alive","lastLatency":82,"failCount":0,"successCount":417,"failThreshold":3,"restartCount":0,"tunnelRunning":true}]}`
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
}
