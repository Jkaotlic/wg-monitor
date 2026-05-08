package callbacks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fakeEditTG struct {
	editText string
	kb       *tg.InlineKeyboardMarkup
}

func (f *fakeEditTG) EditMessageText(ctx context.Context, chatID, msgID int64, text, parseMode string, kb *tg.InlineKeyboardMarkup) error {
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
