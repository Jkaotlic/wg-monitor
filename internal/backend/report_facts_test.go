package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func factsTestServer(t *testing.T) (*db.DB, int64, string, *fakeDisp, *httptest.Server) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tok := "4747474747474747474747474747474747474747474747474747474747474747"
	uid, err := d.Users().Insert("v047", tok, "198.51.100.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	disp := &fakeDisp{db: d}
	srv := httptest.NewServer(NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	}))
	t.Cleanup(srv.Close)
	return d, uid, tok, disp, srv
}

func postFactsReport(t *testing.T, srv *httptest.Server, tok string, rep wire.Report) []byte {
	t.Helper()
	body, _ := json.Marshal(rep)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, out)
	}
	return out
}

func TestReportStoresFactsAndPingRuns(t *testing.T) {
	d, uid, tok, _, srv := factsTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	changed := true
	from := now.Add(-10 * time.Minute)
	postFactsReport(t, srv, tok, wire.Report{
		Timestamp:    now,
		AgentVersion: "v0.47.0",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
		Facts: &wire.ReportFacts{
			Exit: &wire.ExitFacts{At: now, Tunnels: map[string]wire.ExitProbe{
				"awg11": {VPNIP: "203.0.113.7", DirectIP: "198.51.100.4", Changed: &changed, Source: wire.ExitSourceAwgm, At: now},
			}},
			PingRuns: []wire.PingRun{{TunnelID: "awg11", From: from, To: from.Add(time.Minute), Fails: 2}},
		},
	})
	all, err := d.RouterFacts().All(uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all[db.FactExit]; !ok {
		t.Fatalf("факт exit не сохранён: %+v", all)
	}
	runs, _ := d.PingRuns().Since(uid, now.Add(-time.Hour))
	if len(runs) != 1 || runs[0].Fails != 2 {
		t.Fatalf("серии: %+v", runs)
	}
}

func TestReportWithoutFactsLeavesTablesEmpty(t *testing.T) {
	d, uid, tok, _, srv := factsTestServer(t)
	postFactsReport(t, srv, tok, wire.Report{Timestamp: time.Now().UTC(), AgentVersion: "v0.46.0",
		Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}}})
	all, _ := d.RouterFacts().All(uid)
	runs, _ := d.PingRuns().Since(uid, time.Now().Add(-time.Hour))
	if len(all) != 0 || len(runs) != 0 {
		t.Fatalf("старый агент оставил факты: %+v %+v", all, runs)
	}
}

// Три отчёта от хука с падающей проверкой не двигают автомат тревог --
// тот же ряд обычными отчётами двигает.
func TestHookReportDoesNotMoveFSM(t *testing.T) {
	d, uid, tok, disp, srv := factsTestServer(t)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	for i := 0; i < 3; i++ {
		postFactsReport(t, srv, tok, wire.Report{
			Timestamp: base.Add(time.Duration(i) * time.Second), AgentVersion: "v0.47.0", Trigger: wire.TriggerHook,
			Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}, {Name: "dns", Status: "fail"}},
		})
	}
	disp.mu.Lock()
	hookCalls := len(disp.calls)
	disp.mu.Unlock()
	if hookCalls != 0 {
		t.Fatalf("хук-отчёты дошли до автомата тревог: %d вызовов", hookCalls)
	}
	if _, ok, _ := d.Events().LatestEvent(uid, "dns"); !ok {
		t.Fatal("события хук-отчёта не записаны: экран не узнает о смене")
	}
	postFactsReport(t, srv, tok, wire.Report{
		Timestamp: base.Add(10 * time.Second), AgentVersion: "v0.47.0",
		Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}, {Name: "dns", Status: "fail"}},
	})
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) != 1 {
		t.Fatalf("обычный отчёт обязан двигать автомат: %d вызовов", len(disp.calls))
	}
}

func TestReportResponseAdvertisesHookReports(t *testing.T) {
	_, _, tok, _, srv := factsTestServer(t)
	body := postFactsReport(t, srv, tok, wire.Report{Timestamp: time.Now().UTC(), AgentVersion: "v0.47.0",
		Checks: []wire.Check{{Name: "agent_heartbeat", Status: "ok"}}})
	var rr wire.ReportResponse
	if err := json.Unmarshal(body, &rr); err != nil {
		t.Fatalf("ответ не JSON: %q", body)
	}
	if !rr.HookReports {
		t.Fatalf("бэкенд v0.47 обязан объявить hook_reports: %q", body)
	}
}
