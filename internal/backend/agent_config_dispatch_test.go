package backend

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func newAgentConfigMux(t *testing.T) (*db.DB, *dashboardActionSink, http.Handler) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Users().Insert("client-b", "tok", "1.2.3.4", "awg0"); err != nil {
		t.Fatal(err)
	}
	sink := &dashboardActionSink{}
	return d, sink, NewMux(Deps{DB: d, CommandSink: sink, DashboardToken: "secret"})
}

func postDashboardCommand(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/client-b/commands", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDashboardAgentConfigGetIsAllowed(t *testing.T) {
	_, sink, h := newAgentConfigMux(t)
	rec := postDashboardCommand(t, h, `{"action":"agent_config_get","args":{"ignored":1}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 || sink.enqueued[0].Action != "agent_config_get" || len(sink.enqueued[0].Args) != 0 {
		t.Fatalf("bad enqueue: %+v", sink.enqueued)
	}
}

func TestDashboardUpdateAgentConfigSanitizesAndDropsUnknownKeys(t *testing.T) {
	_, sink, h := newAgentConfigMux(t)
	// Mix valid keys with unknown + a backend-url-ish key; only whitelisted keys survive.
	rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{"interval_sec":90,"allow_router_reboot":false,"url":"https://evil.example","backend_url":"https://evil.example","nickname":"hacked"}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 1 {
		t.Fatalf("want 1 enqueue, got %+v", sink.enqueued)
	}
	args := sink.enqueued[0].Args
	if _, ok := args["url"]; ok {
		t.Fatalf("backend url leaked into update_agent_config: %+v", args)
	}
	if _, ok := args["backend_url"]; ok {
		t.Fatalf("backend_url leaked: %+v", args)
	}
	if _, ok := args["nickname"]; ok {
		t.Fatalf("nickname leaked: %+v", args)
	}
	if args["interval_sec"] != 90 {
		t.Fatalf("interval_sec not sanitized through: %+v", args)
	}
	if v, ok := args["allow_router_reboot"].(bool); !ok || v {
		t.Fatalf("allow_router_reboot not preserved as false: %+v", args)
	}
}

func TestDashboardUpdateAgentConfigRejectsBadInterval(t *testing.T) {
	_, sink, h := newAgentConfigMux(t)
	rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{"interval_sec":2}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("invalid config must not enqueue: %+v", sink.enqueued)
	}
}

func TestDashboardUpdateAgentConfigRejectsEmpty(t *testing.T) {
	_, sink, h := newAgentConfigMux(t)
	rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{"unknown":"x"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.enqueued) != 0 {
		t.Fatalf("empty config must not enqueue: %+v", sink.enqueued)
	}
}

// Ключи DNS-сторожа проходят через дашборд с теми же проверками, что делает
// агент: плохое значение -- отказ 400 без постановки в очередь, как у
// остальных ключей (TestDashboardUpdateAgentConfigRejectsBadInterval).
func TestDashboardUpdateAgentConfigSanitizesWatchdogKeys(t *testing.T) {
	t.Run("valid keys pass through", func(t *testing.T) {
		_, sink, h := newAgentConfigMux(t)
		rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{`+
			`"dns_watchdog_enabled":true,`+
			`"dns_watchdog_endpoint":" https://dns.example.com/secret-path ",`+
			`"dns_watchdog_canary_domain":"example.org",`+
			`"dns_watchdog_bootstrap_ip":"198.51.100.7"}}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if len(sink.enqueued) != 1 {
			t.Fatalf("want 1 enqueue, got %+v", sink.enqueued)
		}
		args := sink.enqueued[0].Args
		if v, ok := args["dns_watchdog_enabled"].(bool); !ok || !v {
			t.Fatalf("dns_watchdog_enabled not preserved: %+v", args)
		}
		if args["dns_watchdog_endpoint"] != "https://dns.example.com/secret-path" ||
			args["dns_watchdog_canary_domain"] != "example.org" ||
			args["dns_watchdog_bootstrap_ip"] != "198.51.100.7" {
			t.Fatalf("watchdog keys not sanitized through: %+v", args)
		}
	})

	t.Run("empty bootstrap ip clears it", func(t *testing.T) {
		_, sink, h := newAgentConfigMux(t)
		rec := postDashboardCommand(t, h, `{"action":"update_agent_config","args":{"dns_watchdog_bootstrap_ip":""}}`)
		if rec.Code != http.StatusAccepted || len(sink.enqueued) != 1 {
			t.Fatalf("status=%d body=%s enq=%+v", rec.Code, rec.Body.String(), sink.enqueued)
		}
	})

	bad := map[string]string{
		"http endpoint":          `{"dns_watchdog_endpoint":"http://dns.example.com/secret"}`,
		"empty endpoint":         `{"dns_watchdog_endpoint":""}`,
		"masked endpoint echoed": `{"dns_watchdog_endpoint":"https://dns.example.com/***"}`,
		"endpoint too long":      `{"dns_watchdog_endpoint":"https://dns.example.com/` + strings.Repeat("a", 500) + `"}`,
		"bad ip":                 `{"dns_watchdog_bootstrap_ip":"198.51.100.300"}`,
		"ipv6 bootstrap":         `{"dns_watchdog_bootstrap_ip":"2001:db8::1"}`,
		"bad domain":             `{"dns_watchdog_canary_domain":"exa mple.com"}`,
		"url as domain":          `{"dns_watchdog_canary_domain":"https://example.com"}`,
		"enabled wrong type":     `{"dns_watchdog_enabled":"yes"}`,
	}
	for name, args := range bad {
		t.Run(name, func(t *testing.T) {
			_, sink, h := newAgentConfigMux(t)
			// A valid key rides along: the bad one must still sink the whole
			// request instead of being silently dropped.
			body := `{"action":"update_agent_config","args":` + strings.TrimSuffix(args, "}") + `,"interval_sec":90}}`
			rec := postDashboardCommand(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(sink.enqueued) != 0 {
				t.Fatalf("invalid watchdog config must not enqueue: %+v", sink.enqueued)
			}
		})
	}
}
