//go:build manual

package actions

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Живая проверка переноса правил на песочнице из одного правила.
//
// Песочница нужна потому, что перенос забирает ВСЁ, что ведёт в туннель.
// Здесь правило создаётся на неиспользуемом туннеле, переносится на другой,
// возвращается обратно и удаляется; отдельно проверяется главное — что
// правила политик остались на месте (та самая мина, починенная в v0.17.1).
//
//	AWGM_URL=http://192.168.0.1:2222 REBIND_SRC=awg10 REBIND_DST=awg20 ROUTE_TEMPLATE=figma \
//	  go test -tags manual ./internal/agent/actions/ -run LiveRouteRebind -v
func TestLiveRouteRebind(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	src := os.Getenv("REBIND_SRC")
	dst := os.Getenv("REBIND_DST")
	template := os.Getenv("ROUTE_TEMPLATE")
	if base == "" || src == "" || dst == "" || template == "" {
		t.Skip("нужны AWGM_URL, REBIND_SRC, REBIND_DST, ROUTE_TEMPLATE")
	}
	c := awgmgr.New(base)
	ctx := context.Background()

	// Снимок ДО: все правила и то, чем они привязаны.
	before, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние ДО: %v", err)
	}
	bindsBefore := map[string]string{}
	for _, r := range before {
		bindsBefore[r.ID] = r.Bind
	}
	t.Logf("ДО: правил %d", len(before))

	// Песочница: одно правило на неиспользуемом туннеле.
	plan, err := RouteAddPlanJSON(ctx, c, wire.RouteAddRequest{Kind: "dns", TunnelID: src, TemplateID: template})
	if err != nil {
		t.Fatalf("план: %v", err)
	}
	var p wire.RouteAddPlan
	if err := json.Unmarshal([]byte(plan), &p); err != nil {
		t.Fatal(err)
	}
	if !p.CanApply {
		t.Fatalf("план запрещает применение: %+v", p.Overlaps)
	}
	applyOut, err := RouteAddJSON(ctx, c, wire.RouteAddRequest{Kind: "dns", TunnelID: src, TemplateID: template, DraftHash: p.Hash})
	if err != nil {
		t.Fatalf("создание песочницы: %v", err)
	}
	var applied wire.RouteApplyResult
	if err := json.Unmarshal([]byte(applyOut), &applied); err != nil {
		t.Fatal(err)
	}
	t.Logf("ПЕСОЧНИЦА: %s (%s) на %s", applied.RouteName, applied.RouteID, src)

	cleanup := func() {
		if _, err := RouteDeleteJSON(ctx, c, wire.RouteDeleteRequest{Kind: "dns", RouteID: applied.RouteID}); err != nil {
			t.Errorf("УБОРКА НЕ УДАЛАСЬ: правило %s осталось: %v", applied.RouteID, err)
		}
	}
	defer cleanup()

	out, err := RouteRebind(ctx, c, src, dst)
	if err != nil {
		t.Fatalf("перенос %s -> %s: %v", src, dst, err)
	}
	t.Logf("ПЕРЕНОС: %s", out)

	mid, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние после переноса: %v", err)
	}
	moved := false
	for _, r := range mid {
		if r.ID == applied.RouteID {
			moved = r.Bind != bindsBefore[applied.RouteID]
			t.Logf("песочница теперь на %q", r.Bind)
		}
		// Главное: правила, которых перенос касаться не должен, не изменились.
		if old, ok := bindsBefore[r.ID]; ok && old != r.Bind {
			t.Errorf("ПОСТОРОННЕЕ ПРАВИЛО СМЕНИЛО ПРИВЯЗКУ: %s было %q стало %q", r.ID, old, r.Bind)
		}
	}
	if !moved {
		t.Errorf("песочница не переехала -- перенос ничего не сделал")
	}

	// Откат: возвращаем всё обратно тем же действием.
	if _, err := RouteRebind(ctx, c, dst, src); err != nil {
		t.Fatalf("ОТКАТ НЕ УДАЛСЯ (%s -> %s): %v", dst, src, err)
	}
	after, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние ПОСЛЕ: %v", err)
	}
	for _, r := range after {
		if old, ok := bindsBefore[r.ID]; ok && old != r.Bind {
			t.Errorf("после отката привязка не вернулась: %s было %q стало %q", r.ID, old, r.Bind)
		}
	}
	t.Logf("ПОСЛЕ: правил %d (песочница будет удалена)", len(after))
}
