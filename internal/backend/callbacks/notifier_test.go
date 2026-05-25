package callbacks

import (
	"context"
	"testing"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestNotifier_TunnelResultRefreshesOriginPanel(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)
	n.TunnelsPanelBuilder = func(userID int64) (string, tg.InlineKeyboardMarkup, bool) {
		if userID != 42 {
			t.Fatalf("userID = %d, want 42", userID)
		}
		return "fresh panel", tg.InlineKeyboardMarkup{}, true
	}

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnel_disable"},
		"tunnel_disable",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "interface Wireguard3 -> down"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMsgs) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(f.sentMsgs))
	}
	if len(f.edits) != 1 || f.edits[0] != "fresh panel" {
		t.Fatalf("edits = %#v, want fresh panel edit", f.edits)
	}
}
