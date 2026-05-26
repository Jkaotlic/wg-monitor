package callbacks

import (
	"context"
	"testing"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/pkg/wire"
)

func TestNotifier_TunnelResultQueuesLivePanelRefresh(t *testing.T) {
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{}
	n := NewNotifier(f)
	n.TunnelsRefreshSink = sink

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
	if len(f.edits) != 0 {
		t.Fatalf("must not render stale event-based panel after mutation, edits = %#v", f.edits)
	}
	if len(sink.calls) != 1 || sink.calls[0].action != "tunnels_status" {
		t.Fatalf("expected live tunnels_status refresh, got %+v", sink.calls)
	}
	if len(sink.refs) != 1 || sink.refs[0].chatID != 100 || sink.refs[0].messageID != 200 {
		t.Fatalf("refresh should target the original panel ref, got %+v", sink.refs)
	}
}
