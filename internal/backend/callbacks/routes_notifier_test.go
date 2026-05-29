package callbacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/pkg/wire"
)

type fakeEditTG struct {
	editText string
	kb       *tg.InlineKeyboardMarkup
	calls    int
}

func (f *fakeEditTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
	f.calls++
	f.editText = text
	f.kb = kb
	return nil
}

func TestRoutesPanelNotifier_StatusOK(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d}
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{ID: "t1", Name: "amnezia", Iface: "nwg1"}}}
	body, _ := json.Marshal(snap)
	res := wire.CommandResult{Status: "ok", Output: string(body)}
	ref := cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgFake.editText, "amnezia") {
		t.Errorf("expected panel rendered: %s", tgFake.editText)
	}
	if _, ok := cache.Get(uid); !ok {
		t.Errorf("snapshot must be cached after status fetch")
	}
}

func TestRoutesPanelNotifier_TunnelsStatusOKRendersTunnelsPanel(t *testing.T) {
	d, uid := newTestDB(t)
	tgFake := &fakeEditTG{}
	cache := &RoutesCache{TTL: time.Minute}
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d}
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{{
		ID: "awg10", Name: "live", Iface: "nwg0", NDMSName: "Wireguard0",
		Enabled: true, Status: "running", HasHandshake: true, HandshakeAge: 12,
	}}}
	body, _ := json.Marshal(snap)

	ref := cmdpkg.MessageRef{ChatID: 100, MessageID: 200, Action: "tunnels_status"}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, uid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgFake.editText, "Туннели") || !strings.Contains(tgFake.editText, "live (nwg0)") {
		t.Fatalf("expected tunnels panel text, got %q", tgFake.editText)
	}
	if !keyboardHasCallback(tgFake.kb, "tunnel_disable") {
		t.Fatalf("expected live tunnel toggle callback, got %#v", tgFake.kb)
	}
}

func TestRoutesPanelNotifier_RebindResult(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	// Pre-populate cache so the renderer can resolve src/dst names.
	snap := wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{
		{ID: "t1", Name: "amnezia", Iface: "nwg1"},
		{ID: "t2", Name: "newtun", Iface: "nwg0"},
	}}
	d, uid := newTestDB(t)
	cache.Put(uid, snap)
	tgFake := &fakeEditTG{}
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d}
	rb := wire.RouteRebindResult{
		SrcTunnelID: "t1", DstTunnelID: "t2",
		DNS:    wire.CategoryResult{OK: 3},
		Static: wire.CategoryResult{OK: 1},
	}
	body, _ := json.Marshal(rb)
	res := wire.CommandResult{Status: "ok", Output: string(body)}
	ref := cmdpkg.MessageRef{Action: "route_rebind", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tgFake.editText, "amnezia") || !strings.Contains(tgFake.editText, "newtun") {
		t.Errorf("expected named tunnels in result text: %s", tgFake.editText)
	}
	if _, ok := cache.Get(uid); ok {
		t.Errorf("cache should be invalidated after rebind")
	}
}

func TestRoutesPanelNotifier_RebindStatusErrShowsTelegramError(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d}

	res := wire.CommandResult{Status: "err", Output: "route rebind failed: ip route add: file exists"}
	ref := cmdpkg.MessageRef{Action: "route_rebind", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}
	if tgFake.calls != 1 {
		t.Fatalf("EditMessageText calls = %d, want 1", tgFake.calls)
	}
	if !strings.Contains(tgFake.editText, "Маршруты") {
		t.Errorf("expected routes title in error text: %s", tgFake.editText)
	}
	if !strings.Contains(tgFake.editText, "ошибка") && !strings.Contains(tgFake.editText, "Ошибка") {
		t.Errorf("expected clear error wording: %s", tgFake.editText)
	}
	if !strings.Contains(tgFake.editText, res.Output) {
		t.Errorf("expected raw agent output in error text: %s", tgFake.editText)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_open") {
		t.Fatalf("expected keyboard to include routes_open: %#v", tgFake.kb)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_refresh") {
		t.Fatalf("expected keyboard to include routes_refresh: %#v", tgFake.kb)
	}
}

func TestRoutesPanelNotifier_ApplyResultOffersSnapshot(t *testing.T) {
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	n := &RoutesPanelNotifier{TG: tgFake, Cache: &RoutesCache{TTL: time.Minute}, DB: d}
	body, _ := json.Marshal(wire.RouteApplyResult{
		Action:    "add",
		Kind:      "dns",
		RouteID:   "r1",
		RouteName: "YouTube",
	})
	ref := cmdpkg.MessageRef{Action: "route_add", ChatID: 1, MessageID: 7}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, uid); err != nil {
		t.Fatal(err)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_open") {
		t.Fatalf("expected keyboard to include routes_open: %#v", tgFake.kb)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_refresh") {
		t.Fatalf("expected keyboard to include routes_refresh: %#v", tgFake.kb)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_snapshot") {
		t.Fatalf("expected keyboard to include routes_snapshot: %#v", tgFake.kb)
	}
}

func keyboardHasCallback(kb *tg.InlineKeyboardMarkup, prefix string) bool {
	if kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix+":") {
				return true
			}
		}
	}
	return false
}
