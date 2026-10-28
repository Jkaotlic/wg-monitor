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

// Живая сверка каталога наборов. Только чтение.
//
//	AWGM_URL=http://192.168.0.1:2222 go test -tags manual ./internal/agent/actions/ -run LiveRouteTemplates -v
func TestLiveRouteTemplates(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	if base == "" {
		t.Skip("нужен AWGM_URL")
	}
	out, err := RouteTemplatesJSON(context.Background(), awgmgr.New(base))
	if err != nil {
		t.Fatalf("route_templates: %v", err)
	}
	var got wire.RouteTemplates
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	cats := map[string]int{}
	withHR := 0
	for _, tpl := range got.Templates {
		cats[tpl.Category]++
		if len(tpl.HRNeo) > 0 {
			withHR++
		}
	}
	t.Logf("в каталоге %d наборов, пропущено %d, категорий %d, с гео-тегами %d",
		len(got.Templates), got.Skipped, len(cats), withHR)
	t.Logf("категории: %v", cats)
	if len(got.Templates) == 0 {
		t.Fatal("каталог пуст -- экран покажет пустоту")
	}
}

// Живая сверка гео-тегов и выбора движка. Только чтение: план не пишет на
// роутер ничего (RoutingRefresh и создание -- только в RouteAddJSON).
//
//	AWGM_URL=http://192.168.0.1:2222 AWGM_TUNNEL=<id> go test -tags manual ./internal/agent/actions/ -run LiveRouteGeo -v
func TestLiveRouteGeo(t *testing.T) {
	base := os.Getenv("AWGM_URL")
	if base == "" {
		t.Skip("нужен AWGM_URL")
	}
	c := awgmgr.New(base)
	out, err := RouteGeoTagsJSON(context.Background(), c)
	if err != nil {
		t.Fatalf("route_geo_tags: %v", err)
	}
	var got wire.RouteGeoTags
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, tag := range got.Tags {
		kinds[tag.Kind]++
	}
	t.Logf("гео-тегов %d (%v), ответ %d байт", len(got.Tags), kinds, len(out))
	tunnel := os.Getenv("AWGM_TUNNEL")
	if tunnel == "" {
		return
	}
	for _, probe := range []struct {
		kind    string
		targets []string
	}{{"dns", []string{"example.com"}}, {"dns", []string{"geosite:YOUTUBE", "geoip:YOUTUBE"}}, {"static", []string{"203.0.113.0/24"}}} {
		targets := probe.targets
		plan, err := buildRouteAddPlan(context.Background(), c, wire.RouteAddRequest{Kind: probe.kind, Name: "probe", TunnelID: tunnel, Targets: targets})
		if err != nil {
			t.Fatalf("plan %v: %v", targets, err)
		}
		t.Logf("план %s %v: kind=%s backend=%s use_hr_neo=%v can_apply=%v overlaps=%d", probe.kind, targets, plan.Request.Kind, plan.Route.Backend, plan.Request.UseHRNeo, plan.CanApply, len(plan.Overlaps))
		for _, o := range plan.Overlaps {
			t.Logf("  %s %s %s ← %s", o.Severity, o.Target.Type, o.Target.Value, o.Existing.Name)
		}
	}
}
