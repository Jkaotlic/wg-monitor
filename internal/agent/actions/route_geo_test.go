package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// geoRouteMux -- минимальный роутер для плана и создания правила. hrStatus --
// тело /api/system/hydraroute-status ("" -- эндпоинта нет), existing -- живые
// DNS-правила, created -- куда записать тело создания.
func geoRouteMux(t *testing.T, hrStatus, existing string, created *awgmgr.DNSRoute) *httptest.Server {
	t.Helper()
	if existing == "" {
		existing = "[]"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/routing/tunnels", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"id":"awg1","name":"awg1","iface":"nwg1","type":"managed","status":"running","available":true}]}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":` + existing + `}`))
	})
	mux.HandleFunc("/api/static-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	mux.HandleFunc("/api/dns-routes/create", func(w http.ResponseWriter, r *http.Request) {
		if created != nil {
			if err := json.NewDecoder(r.Body).Decode(created); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/routing/refresh", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if hrStatus != "" {
		mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(hrStatus))
		})
	}
	mux.HandleFunc("/api/system/hydraroute-control", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	return httptest.NewServer(mux)
}

const hrRunning = `{"success":true,"data":{"installed":true,"running":true}}`

// Жалоба оператора 27.10.2026: правила из каталога уходили в NDMS при
// работающем HydraRoute Neo. Экран решал по гео-тегам набора, а с awg-manager
// 2.17.2 их в наборах нет. Движок -- свойство роутера, а не набора: агент
// спрашивает роутер сам.
func TestRouteAddPlan_DNSGoesToHRNeoWhenItRuns(t *testing.T) {
	srv := geoRouteMux(t, hrRunning, "", nil)
	defer srv.Close()
	plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "dns", Name: "Figma", TunnelID: "awg1", Targets: []string{"figma.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Route.Backend != "hydraroute" || !plan.Request.UseHRNeo {
		t.Fatalf("при работающем HR Neo правило должно идти в него: backend=%q use_hr_neo=%v", plan.Route.Backend, plan.Request.UseHRNeo)
	}
}

func TestRouteAddPlan_DNSStaysNDMSWhenHRNeoStopped(t *testing.T) {
	for name, status := range map[string]string{
		"stopped":       `{"success":true,"data":{"installed":true,"running":false}}`,
		"not installed": `{"success":true,"data":{"installed":false,"running":false}}`,
		"no endpoint":   "",
	} {
		t.Run(name, func(t *testing.T) {
			srv := geoRouteMux(t, status, "", nil)
			defer srv.Close()
			plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
				wire.RouteAddRequest{Kind: "dns", Name: "Figma", TunnelID: "awg1", Targets: []string{"figma.com"}})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Route.Backend != "ndms" || plan.Request.UseHRNeo {
				t.Fatalf("без работающего HR Neo -- NDMS: backend=%q", plan.Route.Backend)
			}
		})
	}
}

// Гео-тег разворачивает только HR Neo: в NDMS он стал бы «доменом»
// geosite:youtube, который никогда не совпадёт ни с одним запросом.
func TestRouteAddPlan_GeoTargetsNeedHRNeo(t *testing.T) {
	srv := geoRouteMux(t, `{"success":true,"data":{"installed":true,"running":false}}`, "", nil)
	defer srv.Close()
	_, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "dns", Name: "YT", TunnelID: "awg1", Targets: []string{"geosite:YOUTUBE"}})
	if err == nil || !strings.Contains(err.Error(), "HydraRoute") {
		t.Fatalf("гео-цель без HR Neo должна получить отказ с причиной, got %v", err)
	}
}

// Живые правила workrouter (27.10): geosite:* лежат в domains, geoip:* -- в
// subnets, а manualDomains несёт всё. geoip в domains роутер не развернёт.
func TestRouteAddJSON_GeoRuleSplitsGeoIPIntoSubnets(t *testing.T) {
	var created awgmgr.DNSRoute
	srv := geoRouteMux(t, hrRunning, "", &created)
	defer srv.Close()
	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "dns", Name: "Telegram", TunnelID: "awg1", Targets: []string{"geosite:telegram", "geoip:TELEGRAM", "t.me"}}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	req.DraftHash = plan.Hash
	if _, err := RouteAddJSON(context.Background(), c, req); err != nil {
		t.Fatal(err)
	}
	if created.Backend != "hydraroute" {
		t.Fatalf("backend = %q", created.Backend)
	}
	if strings.Join(created.Domains, ",") != "geosite:TELEGRAM,t.me" {
		t.Fatalf("domains = %v", created.Domains)
	}
	if strings.Join(created.Subnets, ",") != "geoip:TELEGRAM" {
		t.Fatalf("subnets = %v", created.Subnets)
	}
	if len(created.ManualDomains) != 3 {
		t.Fatalf("manualDomains = %v", created.ManualDomains)
	}
}

// Гео-тег -- не домен: «opaque» прятал повтор тега в соседнем правиле. Теги
// сравниваются точным совпадением без учёта регистра.
func TestRouteAddPlan_GeoTagOverlapsSameTag(t *testing.T) {
	existing := `[{"id":"hr:1","name":"Youtubeall","backend":"hydraroute","enabled":true,"domains":["geosite:YOUTUBE"],"manualDomains":["geosite:YOUTUBE"],"routes":[{"interface":"nwg9","tunnelId":"awg9"}]}]`
	srv := geoRouteMux(t, hrRunning, existing, nil)
	defer srv.Close()
	plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "dns", Name: "YT", TunnelID: "awg1", Targets: []string{"geosite:youtube", "geosite:GITHUB"}})
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, o := range plan.Overlaps {
		if o.Target.Type == "opaque" {
			t.Fatalf("гео-тег не должен считаться несравнимым: %+v", o)
		}
		if o.Existing.ID == "hr:1" {
			hits = append(hits, o.Target.Value)
		}
	}
	if strings.Join(hits, ",") != "geosite:YOUTUBE" {
		t.Fatalf("пересечение по тегу: %v (overlaps %+v)", hits, plan.Overlaps)
	}
}

// Два комплекта файлов (свой awg-manager и внешний HydraRoute) несут одни и те
// же теги: экрану нужен один список без повторов, по типам.
func TestRouteGeoTagsJSON_MergesFilesByKindAndName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hydraroute/geo-files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"type":"geosite","path":"/a/geosite.dat"},
			{"type":"geoip","path":"/a/geoip.dat"},
			{"type":"geosite","path":"/b/geosite.dat","external":true},
			{"type":"weird","path":"/c/x.dat"}
		]}`))
	})
	mux.HandleFunc("/api/hydraroute/geo-tags", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("path") {
		case "/a/geosite.dat":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"YOUTUBE","count":40},{"name":"GITHUB","count":30}]}`))
		case "/b/geosite.dat":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"youtube","count":55},{"name":"KINOPUB","count":3}]}`))
		case "/a/geoip.dat":
			_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"YOUTUBE","count":7}]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Query().Get("path"))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	out, err := RouteGeoTagsJSON(context.Background(), awgmgr.New(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	var got wire.RouteGeoTags
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	var flat []string
	for _, tag := range got.Tags {
		flat = append(flat, tag.Kind+":"+tag.Name+"="+itoa(tag.Count))
	}
	want := "geoip:YOUTUBE=7,geosite:GITHUB=30,geosite:KINOPUB=3,geosite:YOUTUBE=55"
	if strings.Join(flat, ",") != want {
		t.Fatalf("tags = %v, want %s", flat, want)
	}
}

func TestRouteGeoTagsJSON_FailsWhenNoFileReadable(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/hydraroute/geo-files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[{"type":"geosite","path":"/a/geosite.dat"}]}`))
	})
	mux.HandleFunc("/api/hydraroute/geo-tags", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"broken"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := RouteGeoTagsJSON(context.Background(), awgmgr.New(srv.URL)); err == nil {
		t.Fatal("ни один файл не прочитан -- это ошибка, а не пустой список")
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// Оператор 28.10.2026: «статические маршруты надо перенести, гидра роут это
// поддерживает». При работающем HR Neo сеть уходит в правило HR Neo (поле
// subnets), а не в статический маршрут NDMS.
func TestRouteAddJSON_StaticGoesToHRNeoSubnetsWhenItRuns(t *testing.T) {
	var created awgmgr.DNSRoute
	srv := geoRouteMux(t, hrRunning, "", &created)
	defer srv.Close()
	staticCreated := false
	srv.Config.Handler.(*http.ServeMux).HandleFunc("/api/static-routes/create", func(w http.ResponseWriter, r *http.Request) {
		staticCreated = true
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	c := awgmgr.New(srv.URL)
	req := wire.RouteAddRequest{Kind: "static", Name: "Офис", TunnelID: "awg1", Targets: []string{"203.0.113.0/24", "198.51.100.7"}}
	plan, err := buildRouteAddPlan(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Request.Kind != "dns" || plan.Route.Kind != "dns" || plan.Route.Backend != "hydraroute" {
		t.Fatalf("сеть при работающем HR Neo -- правило HR Neo: kind=%q backend=%q", plan.Request.Kind, plan.Route.Backend)
	}
	req.DraftHash = plan.Hash
	out, err := RouteAddJSON(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	if staticCreated {
		t.Fatal("статический маршрут NDMS создаваться не должен")
	}
	if created.Backend != "hydraroute" || len(created.Domains) != 0 {
		t.Fatalf("created = %+v", created)
	}
	if strings.Join(created.Subnets, ",") != "203.0.113.0/24,198.51.100.7" {
		t.Fatalf("subnets = %v", created.Subnets)
	}
	if !strings.Contains(out, `"kind":"dns"`) {
		t.Fatalf("result = %s", out)
	}
}

func TestRouteAddPlan_StaticStaysNDMSWithoutHRNeo(t *testing.T) {
	srv := geoRouteMux(t, `{"success":true,"data":{"installed":true,"running":false}}`, "", nil)
	defer srv.Close()
	plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "static", Name: "Офис", TunnelID: "awg1", Targets: []string{"203.0.113.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Request.Kind != "static" || plan.Route.Backend != "" {
		t.Fatalf("без HR Neo -- статический маршрут: %+v", plan.Route)
	}
}

// Сеть в DNS-правиле NDMS не работает: NDMS разворачивает там только имена.
func TestRouteAddPlan_CIDRInNDMSDNSRuleRefused(t *testing.T) {
	srv := geoRouteMux(t, "", "", nil)
	defer srv.Close()
	_, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "dns", Name: "x", TunnelID: "awg1", Targets: []string{"example.com", "203.0.113.0/24"}})
	if err == nil || !strings.Contains(err.Error(), "HydraRoute") {
		t.Fatalf("сеть в DNS-правиле NDMS -- отказ с причиной, got %v", err)
	}
}

// Сети правила HR Neo живут в subnets. Не читать их -- значит не увидеть
// пересечение со своей же сетью и не найти своё правило после создания.
func TestRouteAddPlan_SeesSubnetsOfExistingHRNeoRule(t *testing.T) {
	existing := `[{"id":"hr:9","name":"Офис","backend":"hydraroute","enabled":true,"domains":[],"subnets":["203.0.113.0/24"],"routes":[{"interface":"nwg9","tunnelId":"awg9"}]}]`
	srv := geoRouteMux(t, hrRunning, existing, nil)
	defer srv.Close()
	plan, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "static", Name: "Офис-2", TunnelID: "awg1", Targets: []string{"203.0.113.10"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply || len(plan.Overlaps) == 0 || plan.Overlaps[0].Existing.ID != "hr:9" {
		t.Fatalf("пересечение с сетью правила HR Neo не найдено: %+v", plan.Overlaps)
	}
}

// Домен в static -- ошибка вызывающего, и она должна остаться ошибкой, а не
// тихо стать DNS-правилом HR Neo.
func TestRouteAddPlan_StaticWithDomainStillRefusedWhenHRNeoRuns(t *testing.T) {
	srv := geoRouteMux(t, hrRunning, "", nil)
	defer srv.Close()
	_, err := buildRouteAddPlan(context.Background(), awgmgr.New(srv.URL),
		wire.RouteAddRequest{Kind: "static", Name: "x", TunnelID: "awg1", Targets: []string{"example.com"}})
	if err == nil || !strings.Contains(err.Error(), "must be CIDR or IP") {
		t.Fatalf("static с доменом -- прежний отказ, got %v", err)
	}
}
