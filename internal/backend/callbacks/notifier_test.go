package callbacks

import (
	"context"
	"strings"
	"testing"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
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

func TestNotifier_TunnelImportResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnel_import"},
		"tunnel_import",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: `✅ Туннель "newtun" создан (id=awg99)`},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMsgs) != 0 {
		t.Fatalf("sent messages = %d, want 0; tunnel import result should edit the checking message", len(f.sentMsgs))
	}
	if len(f.edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(f.edits))
	}
	if !strings.Contains(f.edits[0], "newtun") {
		t.Fatalf("edited result should mention imported tunnel, got %q", f.edits[0])
	}
	if len(f.editMarkups) != 1 {
		t.Fatalf("edit markups = %d, want 1", len(f.editMarkups))
	}
	kb := f.editMarkups[0]
	if kb == nil {
		t.Fatal("tunnel import result edit should carry inline next-action keyboard")
	}
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"check_via_tunnel:42:_panel_",
		"routes_open:42:_panel_",
		"pingcheck_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("tunnel import result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_TunnelsStatusResultOffersRouteTransferNextAction(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnels_status"},
		"tunnels_status",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: `✅ amnezia_nl поднят
✅ old_default поднят`},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMarkups) != 1 {
		t.Fatalf("sent markups = %d, want 1", len(f.sentMarkups))
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("tunnels status result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"routes_open:42:_panel_",
		"pingcheck_open:42:_panel_",
		"router_doctor:42:_menu",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("tunnels status result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_TunnelsStatusErrorOffersRecoveryActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnels_status"},
		"tunnels_status",
		wire.CommandResult{ID: "cmd1", Status: "err", Output: "awg-manager timeout"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMarkups) != 1 {
		t.Fatalf("sent markups = %d, want 1", len(f.sentMarkups))
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("tunnels status error should carry inline recovery keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"router_doctor:42:_menu",
		"maint_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("tunnels status error keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_TunnelImportErrorOffersRecoveryActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnel_import"},
		"tunnel_import",
		wire.CommandResult{ID: "cmd1", Status: "err", Output: "awg-manager refused import"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMsgs) != 0 {
		t.Fatalf("sent messages = %d, want 0; tunnel import error should edit the checking message", len(f.sentMsgs))
	}
	if len(f.edits) != 1 {
		t.Fatalf("edits = %d, want 1", len(f.edits))
	}
	kb := f.editMarkups[0]
	if kb == nil {
		t.Fatal("tunnel import error edit should carry inline recovery keyboard")
	}
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"router_doctor:42:_menu",
		"maint_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("tunnel import error keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_RestartTunnelResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "restart_tunnel"},
		"restart_tunnel",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "restart queued"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("restart result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"pingcheck_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("restart result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_TunnelMutationResultOffersNextActions(t *testing.T) {
	for _, action := range []string{"tunnel_enable", "tunnel_disable", "tunnel_delete"} {
		t.Run(action, func(t *testing.T) {
			f := &fakeRouterTG{}
			n := NewNotifier(f)

			err := n.NotifyCommandResult(context.Background(),
				cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: action},
				action,
				wire.CommandResult{ID: "cmd1", Status: "ok", Output: "tunnel mutation ok"},
				42,
				3500,
			)
			if err != nil {
				t.Fatal(err)
			}
			kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
			if !ok || kb == nil {
				t.Fatalf("%s result should carry inline next-action keyboard, got %T", action, f.sentMarkups[0])
			}
			for _, want := range []string{
				"tunnels_refresh:42:_panel_",
				"routes_open:42:_panel_",
				"pingcheck_open:42:_panel_",
			} {
				if !containsStr(flattenKbCallbacks(kb), want) {
					t.Fatalf("%s result keyboard missing %q: %+v", action, want, kb.InlineKeyboard)
				}
			}
		})
	}
}

func TestNotifier_PingCheckResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "pingcheck_now"},
		"pingcheck_now",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "pingcheck ok"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("pingcheck result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"pingcheck_open:42:_panel_",
		"diag_now:42:_menu",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("pingcheck result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_RouterDoctorResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "router_doctor"},
		"router_doctor",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "doctor ok"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("router doctor result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"tunnels_refresh:42:_panel_",
		"routes_open:42:_panel_",
		"maint_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("router doctor result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_RouterDoctorErrorOffersRecoveryActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "router_doctor"},
		"router_doctor",
		wire.CommandResult{ID: "cmd1", Status: "err", Output: "awg-manager refused"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("router doctor error should carry inline recovery keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"router_doctor:42:_menu",
		"tunnels_refresh:42:_panel_",
		"routes_open:42:_panel_",
		"maint_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("router doctor error keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestNotifier_ConnectivityResultOffersNextActions(t *testing.T) {
	for _, action := range []string{"check_via_tunnel", "check_direct"} {
		t.Run(action, func(t *testing.T) {
			f := &fakeRouterTG{}
			n := NewNotifier(f)

			err := n.NotifyCommandResult(context.Background(),
				cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: action},
				action,
				wire.CommandResult{ID: "cmd1", Status: "ok", Output: "connectivity ok"},
				42,
				3500,
			)
			if err != nil {
				t.Fatal(err)
			}
			kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
			if !ok || kb == nil {
				t.Fatalf("%s result should carry inline next-action keyboard, got %T", action, f.sentMarkups[0])
			}
			for _, want := range []string{
				"pingcheck_open:42:_panel_",
				"tunnels_refresh:42:_panel_",
				"router_doctor:42:_menu",
				"routes_open:42:_panel_",
			} {
				if !containsStr(flattenKbCallbacks(kb), want) {
					t.Fatalf("%s result keyboard missing %q: %+v", action, want, kb.InlineKeyboard)
				}
			}
		})
	}
}

func TestNotifier_ConnectivityErrorOffersRecoveryActions(t *testing.T) {
	for _, action := range []string{"check_via_tunnel", "check_direct"} {
		t.Run(action, func(t *testing.T) {
			f := &fakeRouterTG{}
			n := NewNotifier(f)

			err := n.NotifyCommandResult(context.Background(),
				cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: action},
				action,
				wire.CommandResult{ID: "cmd1", Status: "err", Output: "connection refused"},
				42,
				3500,
			)
			if err != nil {
				t.Fatal(err)
			}
			kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
			if !ok || kb == nil {
				t.Fatalf("%s error should carry inline recovery keyboard, got %T", action, f.sentMarkups[0])
			}
			for _, want := range []string{
				"pingcheck_open:42:_panel_",
				"tunnels_refresh:42:_panel_",
				"router_doctor:42:_menu",
				"routes_open:42:_panel_",
				"maint_open:42:_panel_",
			} {
				if !containsStr(flattenKbCallbacks(kb), want) {
					t.Fatalf("%s error keyboard missing %q: %+v", action, want, kb.InlineKeyboard)
				}
			}
		})
	}
}

func TestNotifier_ForceRecheckResultOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewNotifier(f)

	err := n.NotifyCommandResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "force_recheck"},
		"force_recheck",
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "report sent"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("force_recheck result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"router_doctor:42:_menu",
		"tunnels_refresh:42:_panel_",
		"routes_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("force_recheck result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestOpkgResultNotifier_UpgradeSuccessOffersNextActions(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewOpkgResultNotifier(f, UIConfigSnapshot{}, nil, func() string { return "deadbeef" })

	err := n.NotifyOpkgResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "opkg_upgrade"},
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "opkg upgrade ok"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMarkups) != 1 {
		t.Fatalf("sent markups = %d, want 1", len(f.sentMarkups))
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("opkg upgrade result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"maint_open:42:_panel_",
		"router_doctor:42:_menu",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("opkg upgrade result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}

func TestOpkgResultNotifier_FeedDisableSuccessOffersRetryAndMaintenance(t *testing.T) {
	f := &fakeRouterTG{}
	n := NewOpkgResultNotifier(f, UIConfigSnapshot{}, nil, func() string { return "deadbeef" })

	err := n.NotifyOpkgResult(context.Background(),
		cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "opkg_feed_disable"},
		wire.CommandResult{ID: "cmd1", Status: "ok", Output: "feed disabled"},
		42,
		3500,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentMarkups) != 1 {
		t.Fatalf("sent markups = %d, want 1", len(f.sentMarkups))
	}
	kb, ok := f.sentMarkups[0].(*tg.InlineKeyboardMarkup)
	if !ok || kb == nil {
		t.Fatalf("opkg feed disable result should carry inline next-action keyboard, got %T", f.sentMarkups[0])
	}
	for _, want := range []string{
		"opkg_upgrade:42:_menu",
		"maint_open:42:_panel_",
	} {
		if !containsStr(flattenKbCallbacks(kb), want) {
			t.Fatalf("opkg feed disable result keyboard missing %q: %+v", want, kb.InlineKeyboard)
		}
	}
}
