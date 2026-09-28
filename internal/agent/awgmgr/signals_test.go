package awgmgr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, path, body string) (*Client, *[]string) {
	t.Helper()
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(404)
			_, _ = w.Write([]byte("404 page not found"))
			return
		}
		queries = append(queries, r.URL.RawQuery)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &queries
}

func TestTestIPLiveShapeAndNoService(t *testing.T) {
	c, q := serve(t, "/api/test/ip", `{"success":true,"data":{"directIp":"198.51.100.4","vpnIp":"203.0.113.7","endpointIp":"203.0.113.7","ipChanged":true}}`)
	got, err := c.TestIP(context.Background(), "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if got.VPNIP != "203.0.113.7" || got.DirectIP != "198.51.100.4" || !got.IPChanged {
		t.Fatalf("got %+v", got)
	}
	if (*q)[0] != "id=awg11" {
		t.Fatalf("query = %q: service не передаём никогда (С1: метка вместо URL -> 400)", (*q)[0])
	}
}

func TestSignalsEndpoints404AreUnsupported(t *testing.T) {
	c, _ := serve(t, "/nothing", `{}`)
	ctx := context.Background()
	_, e1 := c.TestIP(ctx, "awg11")
	_, e2 := c.PingCheckLogs(ctx)
	_, e3 := c.Logs(ctx, LogsQuery{})
	_, e4 := c.WANStatus(ctx)
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, ErrUnsupportedByRouter) {
			t.Errorf("эндпоинт %d: 404 обязан читаться как «сборка не умеет», got %v", i, err)
		}
	}
}

func TestPingCheckLogsLiveShape(t *testing.T) {
	c, q := serve(t, "/api/pingcheck/logs", `{"success":true,"data":[
		{"timestamp":"2026-09-28T07:44:50Z","tunnelId":"awg11","tunnelName":"macmini(15)","success":true,"latency":115,"failCount":0,"threshold":3,"stateChange":"","error":"","backend":"kernel"},
		{"timestamp":"2026-09-28T07:44:05Z","tunnelId":"awg10","tunnelName":"macoffice","success":false,"failCount":1,"threshold":3,"error":"timeout","backend":"nativewg"}]}`)
	got, err := c.PingCheckLogs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Latency != 115 || got[1].Success || got[1].Error != "timeout" {
		t.Fatalf("got %+v", got)
	}
	if ts, ok := got[0].Time(); !ok || ts.Minute() != 44 {
		t.Fatalf("timestamp не разобран: %v %v", ts, ok)
	}
	if (*q)[0] != "" {
		t.Fatalf("query = %q: limit у API нет (С1), без tunnelId -- все VPN-туннели", (*q)[0])
	}
}

func TestLogsLiveShapeUsesLogsArray(t *testing.T) {
	c, _ := serve(t, "/api/logs", `{"success":true,"data":{"enabled":true,"logs":[{"timestamp":"2026-09-28T06:08:50.10990128Z","level":"warn","group":"routing","subgroup":"proxy","action":"boot","target":"proxy","message":"listen-порт переехал: 12*****.1:9000","repeats":1,"lastSeen":"2026-09-28T06:08:50Z","sanitized":true}],"total":1,"bucket":"app","bufferSize":1,"bufferCapacity":5000,"sanitized":true,"oldestTimestamp":"2026-09-28T06:08:50Z"}}`)
	got, err := c.Logs(context.Background(), LogsQuery{Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Total != 1 || len(got.Logs) != 1 || got.Logs[0].Group != "routing" {
		t.Fatalf("got %+v", got)
	}
	if got.Sanitized == nil || !*got.Sanitized {
		t.Fatal("флаг sanitized страницы потерян")
	}
}

// Главная граница приватности журнала: немаскированный журнал агент не умеет
// даже попросить -- параметра в запросе нет ни при каком наборе фильтров.
func TestLogsNeverSendsSanitize(t *testing.T) {
	c, q := serve(t, "/api/logs", `{"success":true,"data":{"enabled":true,"logs":[],"total":0,"sanitized":true}}`)
	for _, lq := range []LogsQuery{{}, {Level: "error"}, {Group: "tunnel", Limit: 200}, {Level: "info", Group: "server", Limit: 1}} {
		if _, err := c.Logs(context.Background(), lq); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range *q {
		if strings.Contains(raw, "sanitize") {
			t.Fatalf("запрос несёт sanitize: %q", raw)
		}
		if !strings.Contains(raw, "bucket=app") {
			t.Fatalf("запрос без bucket=app: %q", raw)
		}
	}
	if !strings.Contains((*q)[2], "group=tunnel") || !strings.Contains((*q)[2], "limit=200") {
		t.Fatalf("фильтры не доехали: %q", (*q)[2])
	}
}

func TestWANStatusLiveShape(t *testing.T) {
	c, _ := serve(t, "/api/wan/status", `{"success":true,"data":{"interfaces":{
		"eth3":{"up":true,"label":"Подключение Ethernet","priority":58248},
		"cdc_br0":{"up":false,"label":"Huawei Mobile Broadband","priority":36405},
		"apcli0":{"up":false,"label":"Wi-Fi","priority":0}},"anyWANUp":true}}`)
	got, err := c.WANStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.AnyWANUp || got.Interfaces["eth3"].Priority != 58248 || got.Interfaces["cdc_br0"].Up {
		t.Fatalf("got %+v", got)
	}
}
