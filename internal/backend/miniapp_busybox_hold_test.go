package backend

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiniappBusyboxHoldGates(t *testing.T) {
	for _, tc := range []struct {
		version string
		person  int64
		want    int
	}{
		{"v0.66.3", 999, http.StatusAccepted}, {"v0.66.2", 999, http.StatusConflict}, {"v0.66.3", 100, http.StatusNotFound}, {"v0.66.3", 555, http.StatusNotFound},
	} {
		_, uid, _, sink, h := maintenanceFleet(t, tc.version)
		rec := postMiniappCommand(t, h, uid, tc.person, `{"action":"opkg_busybox_hold","args":{"package":"evil","sh":"evil"}}`)
		if rec.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, rec.Code, rec.Body.String())
		}
		if tc.want == http.StatusAccepted && (len(sink.enqueued) != 1 || fmt.Sprint(sink.enqueued[0].Args) != "map[]") {
			t.Fatal(sink.enqueued)
		}
	}
}

func TestDashboardBusyboxHoldAgentFloor(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    int
	}{{"v0.66.3", http.StatusAccepted}, {"v0.66.3-rc2", http.StatusConflict}, {"", http.StatusConflict}} {
		d, _, _, sink, _ := maintenanceFleet(t, tc.version)
		h := NewMux(Deps{DB: d, CommandSink: sink, DashboardToken: "test-dashboard"})
		req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/router-owned/commands", strings.NewReader(`{"action":"opkg_busybox_hold","args":{"package":"evil"}}`))
		req.Header.Set("Authorization", "Bearer test-dashboard")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("version=%q: %d %s", tc.version, rec.Code, rec.Body.String())
		}
		if tc.want != http.StatusAccepted && len(sink.enqueued) > 0 {
			t.Fatal("old agent queued")
		}
	}
}
