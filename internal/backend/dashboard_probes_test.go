package backend

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Проверки только на чтение — адрес выхода напрямую и через VPN-туннель и
// анализ конфига до импорта (awg-manager 2.18). Из дашборда их не пускали:
// прогнать их на живом роутере без Telegram было нечем, а мастер замены
// зовёт их же. На роутере они ничего не меняют.

func dashboardProbeEnv(t *testing.T) (http.Handler, *dashboardActionSink) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Users().Insert("client-b", "tok", "198.51.100.30", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &dashboardActionSink{}
	return NewMux(Deps{DB: d, CommandSink: sink, DashboardToken: "secret"}), sink
}

func postDashboardProbe(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/client-b/commands", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDashboardCommandDispatchAllowsReadOnlyProbes(t *testing.T) {
	h, sink := dashboardProbeEnv(t)
	conf := base64.StdEncoding.EncodeToString([]byte("[Interface]\nPrivateKey = x\n"))
	for _, body := range []string{
		`{"action":"check_direct"}`,
		`{"action":"check_via_tunnel"}`,
		`{"action":"tunnel_analyze","args":{"conf":"` + conf + `","lishnee":"x"}}`,
	} {
		if rec := postDashboardProbe(h, body); rec.Code != http.StatusAccepted {
			t.Errorf("%s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
	}
	var analyzed bool
	for _, c := range sink.enqueued {
		if c.Action != "tunnel_analyze" {
			continue
		}
		analyzed = true
		// До агента доезжает только конфиг: всё прочее из запроса — лишнее.
		if c.Args["conf"] != conf || len(c.Args) != 1 {
			t.Fatalf("tunnel_analyze ушла с аргументами %+v", c.Args)
		}
	}
	if !analyzed {
		t.Fatal("tunnel_analyze не поставлена в очередь")
	}
}

func TestDashboardCommandDispatchRejectsBadAnalyzeConf(t *testing.T) {
	h, sink := dashboardProbeEnv(t)
	// Больше предела конфига, но меньше предела тела запроса: отказать обязан
	// разбор аргументов, а не приёмник тела.
	huge := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 20*1024)))
	for name, body := range map[string]string{
		"без конфига":  `{"action":"tunnel_analyze"}`,
		"не base64":    `{"action":"tunnel_analyze","args":{"conf":"это не base64!"}}`,
		"слишком большой": `{"action":"tunnel_analyze","args":{"conf":"` + huge + `"}}`,
	} {
		if rec := postDashboardProbe(h, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: ждали 400, получили %d: %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("плохой конфиг ушёл агенту: %+v", sink.enqueued)
	}
}
