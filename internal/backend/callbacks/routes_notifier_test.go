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

func TestRoutesPanelNotifier_RebindResultEnqueuesFreshRouteStatus(t *testing.T) {
	cache := &RoutesCache{TTL: time.Minute}
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	cache.Put(uid, wire.RouteSnapshot{Tunnels: []wire.TunnelMeta{
		{ID: "t1", Name: "old", Iface: "nwg1"},
		{ID: "t2", Name: "new", Iface: "nwg2"},
	}})
	sink := &fakeEnqueuer{}
	n := &RoutesPanelNotifier{TG: tgFake, Cache: cache, DB: d, Sink: sink}
	rb := wire.RouteRebindResult{
		SrcTunnelID: "t1", DstTunnelID: "t2",
		DNS: wire.CategoryResult{OK: 1},
	}
	body, _ := json.Marshal(rb)

	ref := cmdpkg.MessageRef{Action: "route_rebind", ChatID: 10, MessageID: 20, ThreadID: ptrInt64(30)}
	if err := n.NotifyCommandResult(context.Background(), ref, wire.CommandResult{Status: "ok", Output: string(body)}, uid); err != nil {
		t.Fatal(err)
	}

	if len(sink.calls) != 1 || sink.calls[0].action != "route_status" {
		t.Fatalf("successful rebind should enqueue fresh route_status, got %+v", sink.calls)
	}
	if len(sink.refs) != 1 || sink.refs[0].chatID != 10 || sink.refs[0].messageID != 20 {
		t.Fatalf("fresh route_status should target the same panel message, refs=%+v", sink.refs)
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

func TestRoutesPanelNotifier_RouteStatusErrorOffersRecoveryActions(t *testing.T) {
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	n := &RoutesPanelNotifier{TG: tgFake, Cache: &RoutesCache{TTL: time.Minute}, DB: d}

	ref := cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 7}
	res := wire.CommandResult{Status: "err", Output: "awg-manager timeout"}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"routes_refresh", "routes_hrneo_doctor", "router_doctor", "maint_open"} {
		if !keyboardHasCallback(tgFake.kb, want) {
			t.Fatalf("route status error keyboard missing %q: %#v", want, tgFake.kb)
		}
	}
}

func TestRoutesPanelNotifier_RouteChangeErrorOffersRecoveryActions(t *testing.T) {
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	n := &RoutesPanelNotifier{TG: tgFake, Cache: &RoutesCache{TTL: time.Minute}, DB: d}

	ref := cmdpkg.MessageRef{Action: "route_add_plan", ChatID: 1, MessageID: 7}
	res := wire.CommandResult{Status: "err", Output: "invalid route target"}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"routes_open", "routes_refresh", "router_doctor", "maint_open"} {
		if !keyboardHasCallback(tgFake.kb, want) {
			t.Fatalf("route change error keyboard missing %q: %#v", want, tgFake.kb)
		}
	}
}

func TestRoutesPanelNotifier_RouteTemplatesRendersTemplateButtons(t *testing.T) {
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	threadID := int64(11)
	store := NewRouteWizardStore(time.Minute)
	store.TokenFunc = fixedTokens("tpl1", "tpl2")
	draft := store.PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &threadID, RouterID: uid,
		Kind: "dns", TunnelID: "awg11", UseHRNeo: true,
	})
	n := &RoutesPanelNotifier{TG: tgFake, Cache: &RoutesCache{TTL: time.Minute}, DB: d, Store: store}
	templates := wire.RouteTemplates{Templates: []wire.RouteTemplate{
		{ID: "youtube", Name: "YouTube", DNS: []string{"youtube.com"}, HRNeo: []string{"geosite:YOUTUBE"}},
		{ID: "telegram", Name: "Telegram", HRNeo: []string{"geosite:TELEGRAM"}},
	}}
	body, _ := json.Marshal(templates)

	ref := cmdpkg.MessageRef{Action: "route_templates", ChatID: 1, MessageID: 7, ThreadID: &threadID}
	res := wire.CommandResult{ID: "route_templates:" + draft.Token + ":cmd1", Status: "ok", Output: string(body)}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tgFake.editText, "YouTube") || !strings.Contains(tgFake.editText, "Telegram") {
		t.Fatalf("expected template names in panel, got %q", tgFake.editText)
	}
	cb := firstRouteCallbackWithPrefix(t, tgFake.kb, "routes_tpl_pick:"+itoa(uid)+":_panel_:"+draft.Token+":")
	args, err := Parse(cb)
	if err != nil {
		t.Fatal(err)
	}
	tok, ok := store.GetTemplateTokenForActor(uid, 12345, &threadID, uid, draft.Token, args.RouteTemplateToken)
	if !ok || tok.TemplateID != "youtube" {
		t.Fatalf("expected first button token to resolve youtube, got %+v ok=%v", tok, ok)
	}
}

func TestRoutesPanelNotifier_RouteTemplatesPaginatesAllTemplates(t *testing.T) {
	tgFake := &fakeEditTG{}
	d, uid := newTestDB(t)
	threadID := int64(11)
	store := NewRouteWizardStore(time.Minute)
	store.TokenFunc = fixedTokens("tpl1", "tpl2", "tpl3", "tpl4", "tpl5", "tpl6", "tpl7", "tpl8")
	draft := store.PutAddDraft(RouteAddDraft{
		UserID: uid, ActorTGID: 12345, ThreadID: &threadID, RouterID: uid,
		Kind: "dns", TunnelID: "awg11",
	})
	n := &RoutesPanelNotifier{TG: tgFake, Cache: &RoutesCache{TTL: time.Minute}, DB: d, Store: store}
	templates := wire.RouteTemplates{Templates: []wire.RouteTemplate{}}
	for i := 1; i <= 11; i++ {
		templates.Templates = append(templates.Templates, wire.RouteTemplate{
			ID: "svc" + itoa(int64(i)), Name: "Service " + itoa(int64(i)), DNS: []string{"svc" + itoa(int64(i)) + ".example"},
		})
	}
	body, _ := json.Marshal(templates)

	ref := cmdpkg.MessageRef{Action: "route_templates", ChatID: 1, MessageID: 7, ThreadID: &threadID}
	res := wire.CommandResult{ID: "route_templates:" + draft.Token + ":cmd1", Status: "ok", Output: string(body)}
	if err := n.NotifyCommandResult(context.Background(), ref, res, uid); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(tgFake.editText, "Страница: 1/2") {
		t.Fatalf("expected page counter, got %q", tgFake.editText)
	}
	if strings.Contains(tgFake.editText, "Service 9") {
		t.Fatalf("page 1 should not render service 9 yet: %q", tgFake.editText)
	}
	if !keyboardHasCallback(tgFake.kb, "routes_tpl_page") {
		t.Fatalf("expected next-page button, got %#v", tgFake.kb)
	}
	if countRouteCallbacksWithPrefix(tgFake.kb, "routes_tpl_pick") != routeTemplatesPageSize {
		t.Fatalf("expected %d template buttons on page 1, got %#v", routeTemplatesPageSize, tgFake.kb)
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
	for _, want := range []string{"tunnels_refresh", "check_via_tunnel", "pingcheck_open", "router_doctor"} {
		if !keyboardHasCallback(tgFake.kb, want) {
			t.Fatalf("route apply result keyboard should include post-route verification %q: %#v", want, tgFake.kb)
		}
	}
}

func firstRouteCallbackWithPrefix(t *testing.T, kb *tg.InlineKeyboardMarkup, prefix string) string {
	t.Helper()
	if kb == nil {
		t.Fatalf("nil keyboard, want callback prefix %q", prefix)
	}
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix) {
				return btn.CallbackData
			}
		}
	}
	t.Fatalf("keyboard missing callback prefix %q: %+v", prefix, kb.InlineKeyboard)
	return ""
}

func countRouteCallbacksWithPrefix(kb *tg.InlineKeyboardMarkup, prefix string) int {
	if kb == nil {
		return 0
	}
	n := 0
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, prefix+":") {
				n++
			}
		}
	}
	return n
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
