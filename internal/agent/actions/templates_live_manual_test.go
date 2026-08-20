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
