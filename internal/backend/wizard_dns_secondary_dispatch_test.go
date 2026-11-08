package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Exercise the real HTTP dispatch chain: an ignored mode must never become a
// broad reset, and older agents must not receive a mode they silently ignore.
func TestDNSSecondaryHTTPDispatchPreservesModeAndGatesAgent(t *testing.T) {
	d, uid, _, _ := seedMiniappFleet(t)
	user, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, version string
		args          map[string]any
		status        int
		want          map[string]any
	}{
		{"preview", "v0.66.3-rc1", map[string]any{"mode": "add_secondary", "dry_run": true, "upstream": "evil"}, 202, map[string]any{"mode": "add_secondary", "dry_run": true}},
		{"apply", "v0.66.3-rc1", map[string]any{"mode": "add_secondary", "dry_run": false}, 202, map[string]any{"mode": "add_secondary", "dry_run": false}},
		{"stable", "v0.66.3", map[string]any{"mode": "add_secondary", "dry_run": true}, 202, map[string]any{"mode": "add_secondary", "dry_run": true}},
		{"newer", "v0.67.0", map[string]any{"mode": "add_secondary"}, 202, map[string]any{"mode": "add_secondary", "dry_run": false}},
		{"legacy preview", "v0.31.0", map[string]any{"dry_run": true}, 202, map[string]any{"dry_run": true}},
		{"old", "v0.66.2", map[string]any{"mode": "add_secondary", "dry_run": true}, 409, nil},
		{"pre-floor RC", "v0.66.3-rc0", map[string]any{"mode": "add_secondary", "dry_run": false}, 409, nil},
		{"unknown version", "", map[string]any{"mode": "add_secondary", "dry_run": true}, 409, nil},
		{"unparseable version", "dev", map[string]any{"mode": "add_secondary", "dry_run": true}, 409, nil},
	}
	for _, bad := range []any{"", "unknown", " add_secondary ", nil, 1, true, []any{"add_secondary"}, map[string]any{}} {
		cases = append(cases, struct {
			name, version string
			args          map[string]any
			status        int
			want          map[string]any
		}{fmt.Sprintf("bad mode %#v", bad), "v0.66.3-rc1", map[string]any{"mode": bad, "dry_run": true}, 400, nil})
	}
	for _, surface := range []string{"dashboard", "wizard", "miniapp"} {
		for _, tc := range cases {
			t.Run(surface+"/"+tc.name, func(t *testing.T) {
				var storedVersion any = tc.version
				if tc.version == "" {
					storedVersion = nil
				}
				if _, err := d.SQL().Exec(`UPDATE users SET last_deployed_version=? WHERE id=?`, storedVersion, uid); err != nil {
					t.Fatal(err)
				}
				sink := &dashboardActionSink{}
				h := NewMux(Deps{DB: d, CommandSink: sink, DashboardToken: "secret", WizardToken: "secret", TelegramBotToken: "test-bot-token", TelegramAdminUserID: 999})
				path := "/v1/" + surface + "/agents/" + user.Nickname + "/commands"
				if surface == "miniapp" {
					path = fmt.Sprintf("/v1/miniapp/routers/%d/commands", uid)
				}
				b, err := json.Marshal(map[string]any{"action": "dns_reset", "args": tc.args, "confirm": user.Nickname})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer secret")
				if surface == "miniapp" {
					req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", 999))
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				wantStatus := tc.status
				if surface == "wizard" {
					wantStatus = 400
				}
				if rec.Code != wantStatus {
					t.Fatalf("HTTP %d want %d: %s", rec.Code, wantStatus, rec.Body.String())
				}
				if wantStatus != 202 {
					code := "invalid_mode"
					if surface == "wizard" {
						code = "unsupported_command"
					} else if wantStatus == 409 {
						code = "agent_too_old"
					}
					if !strings.Contains(rec.Body.String(), code) {
						t.Fatalf("wrong rejection: %s", rec.Body.String())
					}
					if len(sink.enqueued) != 0 {
						t.Fatalf("rejected request enqueued %+v", sink.enqueued)
					}
					return
				}
				if len(sink.enqueued) != 1 || !reflect.DeepEqual(sink.enqueued[0].Args, tc.want) {
					t.Fatalf("queue %+v want args %#v", sink.enqueued, tc.want)
				}
			})
		}
	}
}
