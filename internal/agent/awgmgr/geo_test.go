package awgmgr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Форма ответа снята с живого awg-manager 2.18.2 (11.09.2026).
func TestGeoExpand_ParsesLines(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"count":3,"lines":[".anthropic.com",".clau.de",".claude.ai"],"path":"/opt/etc/awg-manager/geo/geosite_GA.dat"}}`))
	}))
	defer srv.Close()

	lines, err := New(srv.URL).GeoExpand(context.Background(), "geosite", "ANTHROPIC")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/hydraroute/geo-expand" || gotQuery != "kind=geosite&tag=ANTHROPIC" {
		t.Fatalf("запрос: %s?%s", gotPath, gotQuery)
	}
	want := []string{".anthropic.com", ".clau.de", ".claude.ai"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
}

// Тег приходит из правила роутера, а не от нас: всё, что в нём есть, обязано
// доехать до роутера одним значением, а не развалить строку запроса.
func TestGeoExpand_EscapesQuery(t *testing.T) {
	var gotTag string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTag = r.URL.Query().Get("tag")
		_, _ = w.Write([]byte(`{"success":true,"data":{"count":0,"lines":[]}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).GeoExpand(context.Background(), "geosite", "A&kind=geoip B"); err != nil {
		t.Fatal(err)
	}
	if gotTag != "A&kind=geoip B" {
		t.Fatalf("tag = %q", gotTag)
	}
}

func TestGeoExpand_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"tag not found"}`))
	}))
	defer srv.Close()
	lines, err := New(srv.URL).GeoExpand(context.Background(), "geosite", "NOPE")
	if err == nil {
		t.Fatalf("success=false должен быть ошибкой, получили %v", lines)
	}
	if !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("ошибка обязана назвать тег: %v", err)
	}
}

// Общий потолок get -- 1 МиБ, а крупные списки категорий больше. Обрезанный
// JSON -- это ошибка разбора, то есть «роутер не раскрыл список» там, где он
// его раскрыл.
func TestGeoExpand_BodyOverOneMiB(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"success":true,"data":{"lines":[`)
	const n = 60000
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `".site-number-%06d.example.com"`, i)
	}
	b.WriteString(`]}}`)
	body := b.String()
	if len(body) <= 1<<20 {
		t.Fatalf("тело %d байт не больше 1 МиБ -- тест ничего не проверяет", len(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	lines, err := New(srv.URL).GeoExpand(context.Background(), "geosite", "CATEGORY-BIG")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != n {
		t.Fatalf("len = %d, want %d", len(lines), n)
	}
}

// Формы ответов сняты с живого awg-manager (workrouter, 27.10.2026): у роутера
// два комплекта файлов -- свой и внешний HydraRoute, -- поэтому файлов четыре.
func TestGeoFiles_ParsesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hydraroute/geo-files" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"type":"geoip","path":"/opt/etc/awg-manager/geo/geoip_GA.dat","tagCount":311},
			{"type":"geosite","path":"/opt/etc/awg-manager/geo/geosite_GA.dat","tagCount":1485,"external":false}
		]}`))
	}))
	defer srv.Close()
	files, err := New(srv.URL).GeoFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Type != "geoip" || files[1].Path != "/opt/etc/awg-manager/geo/geosite_GA.dat" || files[1].TagCount != 1485 {
		t.Fatalf("files = %+v", files)
	}
}

func TestGeoTags_ParsesAndEscapesPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hydraroute/geo-tags" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotPath = r.URL.Query().Get("path")
		_, _ = w.Write([]byte(`{"success":true,"data":[{"name":"ADOBE","count":33},{"name":"AE","count":8043}]}`))
	}))
	defer srv.Close()
	tags, err := New(srv.URL).GeoTags(context.Background(), "/opt/etc/geo files/geoip&x.dat")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/opt/etc/geo files/geoip&x.dat" {
		t.Fatalf("path param = %q", gotPath)
	}
	if len(tags) != 2 || tags[1].Name != "AE" || tags[1].Count != 8043 {
		t.Fatalf("tags = %+v", tags)
	}
}

func TestGeoTags_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"no such file"}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL).GeoTags(context.Background(), "/nope"); err == nil {
		t.Fatal("want error on success=false")
	}
}
