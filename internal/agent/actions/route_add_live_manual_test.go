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

// Живая обкатка «Отправить в туннель» и «Убрать» -- Task 8 плана управления
// маршрутами. Мутация обратимая: правило создаётся и тут же удаляется, а
// состояние сверяется ДО и ПОСЛЕ по числу правил и по самому правилу.
//
// Идёт ровно тем путём, каким ходит приложение: сначала план (агент считает
// пересечения), потом применение с хешем плана, потом план удаления и
// удаление с хешем превью.
//
//	AWGM_URL=http://192.168.0.1:2222 ROUTE_TUNNEL=awg12 ROUTE_TEMPLATE=figma \
//	  go test -tags manual ./internal/agent/actions/ -run LiveRouteAddDelete -v
func TestLiveRouteAddDelete(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	tunnel := os.Getenv("ROUTE_TUNNEL")
	template := os.Getenv("ROUTE_TEMPLATE")
	if base == "" || tunnel == "" || template == "" {
		t.Skip("нужны AWGM_URL, ROUTE_TUNNEL, ROUTE_TEMPLATE")
	}
	c := awgmgr.New(base)
	ctx := context.Background()

	before, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние ДО: %v", err)
	}
	t.Logf("ДО: правил на роутере %d", len(before))

	planJSON, err := RouteAddPlanJSON(ctx, c, wire.RouteAddRequest{
		Kind: "dns", TunnelID: tunnel, TemplateID: template,
	})
	if err != nil {
		t.Fatalf("план добавления: %v", err)
	}
	var plan wire.RouteAddPlan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		t.Fatalf("план не разбирается: %v", err)
	}
	t.Logf("ПЛАН: %s -> %s, целей %d, can_apply=%v, пересечений %d, hash=%s",
		plan.Route.Name, plan.Route.Bind, len(plan.Route.Targets), plan.CanApply, len(plan.Overlaps), plan.Hash)
	for _, o := range plan.Overlaps {
		t.Logf("  пересечение [%s] %s", o.Severity, o.Reason)
	}
	if !plan.CanApply {
		t.Fatalf("план запрещает применение -- выбери другой набор")
	}

	applyJSON, err := RouteAddJSON(ctx, c, wire.RouteAddRequest{
		Kind: "dns", TunnelID: tunnel, TemplateID: template, DraftHash: plan.Hash,
	})
	if err != nil {
		t.Fatalf("ПРИМЕНЕНИЕ не удалось: %v", err)
	}
	var applied wire.RouteApplyResult
	if err := json.Unmarshal([]byte(applyJSON), &applied); err != nil {
		t.Fatalf("ответ применения не разбирается: %v", err)
	}
	t.Logf("СОЗДАНО: %s (%s), HR перезапущен=%v, warning=%q",
		applied.RouteName, applied.RouteID, applied.HRNeoRestarted, applied.Warning)

	mid, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние после создания: %v", err)
	}
	if len(mid) != len(before)+1 {
		t.Errorf("правил стало %d, ожидалось %d", len(mid), len(before)+1)
	}

	// --- откат: удаляем ровно то, что создали ---------------------------
	delPlanJSON, err := RouteDeletePlanJSON(ctx, c, wire.RouteDeleteRequest{Kind: "dns", RouteID: applied.RouteID})
	if err != nil {
		t.Fatalf("ОТКАТ: план удаления не собрался: %v", err)
	}
	var delPlan wire.RouteDeletePlan
	if err := json.Unmarshal([]byte(delPlanJSON), &delPlan); err != nil {
		t.Fatalf("ОТКАТ: план удаления не разбирается: %v", err)
	}
	t.Logf("ПЛАН УДАЛЕНИЯ: %s, целей %d, предупреждений %d",
		delPlan.Route.Name, len(delPlan.Route.Targets), len(delPlan.Warnings))

	if _, err := RouteDeleteJSON(ctx, c, wire.RouteDeleteRequest{
		Kind: "dns", RouteID: applied.RouteID, PreviewHash: delPlan.Hash,
	}); err != nil {
		t.Fatalf("ОТКАТ НЕ УДАЛСЯ: правило %s осталось на роутере: %v", applied.RouteID, err)
	}

	after, err := liveRouteSummaries(ctx, c)
	if err != nil {
		t.Fatalf("состояние ПОСЛЕ: %v", err)
	}
	t.Logf("ПОСЛЕ: правил на роутере %d", len(after))
	if len(after) != len(before) {
		t.Fatalf("постусловие не совпало: было %d, стало %d", len(before), len(after))
	}
	for _, r := range after {
		if r.ID == applied.RouteID {
			t.Fatalf("созданное правило %s всё ещё на роутере", applied.RouteID)
		}
	}
}
