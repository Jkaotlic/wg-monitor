package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type fakeDisp struct {
	mu     sync.Mutex
	calls  []state.Kind
	checks []wire.Check
	db     *db.DB
}

func (f *fakeDisp) Handle(_ context.Context, uid int64, _, check string, tr state.Transition, c wire.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tr.Kind)
	f.checks = append(f.checks, c)
	if f.db != nil {
		return f.db.State().Save(uid, check, tr.Next)
	}
	return nil
}

type fakeResumer struct {
	mu      sync.Mutex
	resumed []int64
}

func (f *fakeResumer) MarkResumed(uid int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumed = append(f.resumed, uid)
}

func TestReportPersistsEventsAndDispatches(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0000000000000000000000000000000000000000000000000000000000000000"
	uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks: []wire.Check{
			{Name: "agent_heartbeat", Status: "ok"},
			{Name: "awg_handshake", Status: "fail", Details: map[string]any{"error": "stale"}},
		},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher invoked %d times (heartbeat must NOT trigger)", len(disp.calls))
	}
	if disp.calls[0] != state.Soft {
		t.Fatalf("kind: %v", disp.calls[0])
	}
	disp.mu.Unlock()

	latest, _ := d.Events().LatestPerUser(uid)
	if latest.IsZero() {
		t.Fatal("event not persisted")
	}
}

func TestReportRejectsInvalidChecksBeforePersisting(t *testing.T) {
	cases := []struct {
		name   string
		checks []wire.Check
	}{
		{
			name:   "empty check name",
			checks: []wire.Check{{Name: "", Status: "ok"}},
		},
		{
			name:   "whitespace check name",
			checks: []wire.Check{{Name: "bad name", Status: "ok"}},
		},
		{
			name:   "unknown status",
			checks: []wire.Check{{Name: "dns", Status: "warn"}},
		},
		{
			name: "duplicate check name",
			checks: []wire.Check{
				{Name: "dns", Status: "ok"},
				{Name: "dns", Status: "fail"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
			defer d.Close()
			tok := "0101010101010101010101010101010101010101010101010101010101010101"
			uid, _ := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

			mux := NewMux(Deps{
				Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				DB:         d,
				Dispatcher: &fakeDisp{db: d},
				Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
			})
			body, _ := json.Marshal(wire.Report{
				Timestamp:    time.Now().UTC(),
				AgentVersion: "test",
				Checks:       tc.checks,
			})
			req := httptest.NewRequest(http.MethodPost, "/v1/report", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
			}
			if got, err := d.Events().LatestPerUser(uid); err != nil {
				t.Fatal(err)
			} else if !got.IsZero() {
				t.Fatalf("invalid report persisted event at %v", got)
			}
		})
	}
}

func TestReportSkipsFSMForOutOfOrderTunnelOK(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2020202020202020202020202020202020202020202020202020202020202020"
	uid, _ := d.Users().Insert("client-a", tok, "1.1.1.1", "awg0")

	fresh := time.Now().UTC().Truncate(time.Second)
	old := fresh.Add(-5 * time.Minute)
	hardSince := fresh.Add(-15 * time.Minute)
	if err := d.State().Save(uid, "tunnel_awg11", db.IncidentState{
		CurrentStatus:    "hard",
		ConsecutiveFails: 4,
		ConsecutiveOKs:   1,
		HardSince:        &hardSince,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Events().Insert(uid, "tunnel_awg11", "fail", `{"tunnel_name":"de","interface":"nwg1","status":"starting"}`, fresh); err != nil {
		t.Fatal(err)
	}

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    old,
		AgentVersion: "test",
		Checks: []wire.Check{{
			Name:   "tunnel_awg11",
			Status: "ok",
			Details: map[string]any{
				"tunnel_name": "de",
				"interface":   "nwg1",
				"status":      "running",
			},
		}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	if len(disp.calls) != 0 {
		t.Fatalf("stale report must not dispatch FSM, calls=%v", disp.calls)
	}
	disp.mu.Unlock()

	got, err := d.State().Get(uid, "tunnel_awg11")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStatus != "hard" || got.ConsecutiveOKs != 1 {
		t.Fatalf("stale OK changed state: status=%q oks=%d", got.CurrentStatus, got.ConsecutiveOKs)
	}
}

func TestReportSilentlyClearsHardTunnelWhenDisabled(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3030303030303030303030303030303030303030303030303030303030303030"
	uid, _ := d.Users().Insert("del", tok, "1.1.1.1", "awg0")

	hardSince := time.Now().UTC().Add(-5 * time.Hour)
	if err := d.State().Save(uid, "tunnel_awg12", db.IncidentState{
		CurrentStatus:    "hard",
		ConsecutiveFails: 7,
		ConsecutiveOKs:   1,
		HardSince:        &hardSince,
	}); err != nil {
		t.Fatal(err)
	}

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC().Truncate(time.Second),
		AgentVersion: "test",
		Checks: []wire.Check{{
			Name:   "tunnel_awg12",
			Status: "ok",
			Details: map[string]any{
				"tunnel_name": "ch",
				"interface":   "nwg2",
				"enabled":     false,
				"status":      "stopped",
				"note":        "tunnel disabled in config",
			},
		}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	if len(disp.calls) != 0 {
		t.Fatalf("disabled tunnel must not dispatch recovery, calls=%v", disp.calls)
	}
	disp.mu.Unlock()

	got, err := d.State().Get(uid, "tunnel_awg12")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStatus != "ok" || got.HardSince != nil || got.ConsecutiveFails != 0 {
		t.Fatalf("disabled tunnel should silently clear hard state, got %+v", got)
	}
}

func TestReportDoesNotRegressLastSeenOrVersionFromOldReport(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2121212121212121212121212121212121212121212121212121212121212121"
	uid, _ := d.Users().Insert("testkeen", tok, "1.1.1.1", "awg0")

	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{db: d},
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	post := func(ts time.Time, version string) {
		t.Helper()
		body, _ := json.Marshal(wire.Report{
			Timestamp:    ts,
			AgentVersion: version,
			Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
		})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	}

	fresh := time.Now().UTC().Truncate(time.Second)
	old := fresh.Add(-10 * time.Minute)
	post(fresh, "v0.13.0-rc30")
	post(old, "v0.13.0-rc14")

	got, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenAt == nil || got.LastSeenAt.UTC().Before(fresh) {
		t.Fatalf("old report regressed last_seen_at: got %v want >= %v", got.LastSeenAt, fresh)
	}
	if got.LastDeployedVersion == nil || *got.LastDeployedVersion != "v0.13.0-rc30" {
		t.Fatalf("old report regressed version: got %v", got.LastDeployedVersion)
	}
}

func TestReportClampsFutureTimestamp(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "2222222222222222222222222222222222222222222222222222222222222222"
	uid, _ := d.Users().Insert("futurebox", tok, "1.1.1.1", "awg0")

	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{db: d},
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	before := time.Now().UTC()
	body, _ := json.Marshal(wire.Report{
		Timestamp:    before.Add(24 * time.Hour),
		AgentVersion: "test",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	after := time.Now().UTC().Add(2 * time.Second)

	latest, ok, err := d.Events().LatestEvent(uid, "agent_heartbeat")
	if err != nil || !ok {
		t.Fatalf("latest heartbeat: ok=%v err=%v", ok, err)
	}
	if latest.TS.After(after) {
		t.Fatalf("future report timestamp was stored as latest event: got %v, after=%v", latest.TS, after)
	}
	got, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenAt == nil || got.LastSeenAt.After(after) {
		t.Fatalf("future report timestamp leaked into last_seen_at: got %v, after=%v", got.LastSeenAt, after)
	}
}

func TestReportMobileUsesHigherFailThreshold(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "abababababababababababababababababababababababababababababababab"
	_, _ = d.Users().InsertWithKind("car4", tok, "1.1.1.1", "awg0", db.KindMobile)

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:                  d,
		Dispatcher:          disp,
		Thresholds:          state.Thresholds{Fail: 2, Recovery: 2},
		MobileFailThreshold: 6,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	postFail := func(i int) {
		t.Helper()
		body, _ := json.Marshal(wire.Report{
			Timestamp:    time.Now().UTC().Add(time.Duration(i) * time.Second),
			AgentVersion: "test",
			Checks:       []wire.Check{{Name: "awg_handshake", Status: "fail"}},
		})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("report %d status: %d", i, resp.StatusCode)
		}
	}

	for i := 1; i <= 5; i++ {
		postFail(i)
	}
	disp.mu.Lock()
	for _, kind := range disp.calls {
		if kind == state.Hard {
			t.Fatalf("mobile agent hardened before mobile threshold: calls=%v", disp.calls)
		}
	}
	disp.mu.Unlock()

	postFail(6)
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if got := disp.calls[len(disp.calls)-1]; got != state.Hard {
		t.Fatalf("6th mobile failure should harden, got %v (calls=%v)", got, disp.calls)
	}
}

func TestReportMobileResumedSuppressesStartupCheckFailures(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "acacacacacacacacacacacacacacacacacacacacacacacacacacacacacacacac"
	uid, _ := d.Users().InsertWithKind("car4", tok, "1.1.1.1", "nwg0", db.KindMobile)

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 2, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Resumed:      true,
		Checks: []wire.Check{
			{Name: "tunnel_awg13", Status: "fail"},
			{Name: "dns", Status: "fail"},
			{Name: "hydraroute", Status: "fail"},
			{Name: "awg_manager", Status: "fail"},
			{Name: "tunnels", Status: "fail"},
			{Name: "awg_handshake", Status: "fail"},
		},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	disp.mu.Lock()
	gotCalls := append([]state.Kind(nil), disp.calls...)
	disp.mu.Unlock()
	if len(gotCalls) != 1 || gotCalls[0] != state.Soft {
		t.Fatalf("only non-startup check should dispatch on mobile resume, got %v", gotCalls)
	}

	for _, check := range []string{"tunnel_awg13", "dns", "hydraroute", "awg_manager", "tunnels"} {
		st, err := d.State().Get(uid, check)
		if err != nil {
			t.Fatal(err)
		}
		if st.ConsecutiveFails != 0 || st.CurrentStatus != "ok" {
			t.Fatalf("startup check %s changed FSM state on resumed report: %+v", check, st)
		}
	}
	if _, ok, err := d.Events().LatestEvent(uid, "hydraroute"); err != nil || !ok {
		t.Fatalf("suppressed startup check must still persist event, ok=%v err=%v", ok, err)
	}
}

func TestReportResumedCallsResumer(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1212121212121212121212121212121212121212121212121212121212121212"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)

	disp := &fakeDisp{}
	resumer := &fakeResumer{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Resumer:    resumer,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Resumed:      true,
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resumer.mu.Lock()
	defer resumer.mu.Unlock()
	if len(resumer.resumed) != 1 || resumer.resumed[0] != uid {
		t.Fatalf("expected MarkResumed(%d), got calls=%v", uid, resumer.resumed)
	}
}

func TestReportNotResumedSkipsResumer(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1313131313131313131313131313131313131313131313131313131313131313"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	resumer := &fakeResumer{}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		Resumer:    resumer,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC(),
		AgentVersion: "test",
		Checks:       []wire.Check{{Name: "agent_heartbeat", Status: "ok"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	resumer.mu.Lock()
	defer resumer.mu.Unlock()
	if len(resumer.resumed) != 0 {
		t.Fatalf("MarkResumed must not be called when Resumed=false; calls=%v", resumer.resumed)
	}
}

// resolver_guard -- не шумная проверка. Дребезг провалов отсеял сам агент
// (провалы подряд и cooldown перед переходом), и его fail -- уже случившийся
// переход роутера на запасные DNS-серверы. Ждать ещё нескольких отчётов,
// прежде чем сказать об этом, бэкенду незачем: порог провала 1.
//
// Порог возврата -- обычный. no_live_fallback решается, пока роутер ещё на
// своём DNS-сервере, и одна удачная проба без всякого cooldown делает
// проверку ok. С порогом возврата 1 перемежающийся сервер (а там, где
// провайдер режет запасные, так выглядит любой отказ) слал бы пару
// HARD/восстановление каждые несколько отчётов.
func TestResolverGuardUsesThresholdOne(t *testing.T) {
	base := state.Thresholds{Fail: 3, Recovery: 2}
	policy := AlertPolicy{NoisyFailThreshold: 6, NoisyRecoveryThreshold: 3}
	if isNoisyCheck("resolver_guard") {
		t.Fatal("resolver_guard must not be treated as a noisy dns_* check")
	}
	if got := thresholdsForCheck(base, policy, "resolver_guard"); got.Fail != 1 || got.Recovery != base.Recovery {
		t.Fatalf("resolver_guard thresholds=%+v, want Fail=1 Recovery=%d (base)", got, base.Recovery)
	}
	// Остальные проверки своих порогов не теряют.
	if got := thresholdsForCheck(base, policy, "hydraroute"); got != base {
		t.Fatalf("hydraroute thresholds changed: %+v", got)
	}
	if got := thresholdsForCheck(base, policy, "dns"); got.Fail != 6 || got.Recovery != 3 {
		t.Fatalf("dns lost its noisy thresholds: %+v", got)
	}
}

// Через отчёты: переход ok→fail в автомате всегда мягкий, так что порог 1
// означает HARD на втором отчёте подряд, а не на третьем, как у остальных.
func TestReportResolverGuardGoesHardOnSecondFail(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3232323232323232323232323232323232323232323232323232323232323232"
	_, _ = d.Users().Insert("guardbox", tok, "198.51.100.9", "awg0")

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  disp,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
		AlertPolicy: AlertPolicy{NoisyFailThreshold: 6, NoisyRecoveryThreshold: 3},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	baseTS := time.Now().UTC()
	seq := 0
	post := func(t *testing.T) {
		t.Helper()
		seq++
		body, _ := json.Marshal(wire.Report{
			Timestamp:    baseTS.Add(time.Duration(seq) * time.Second),
			AgentVersion: "test",
			Checks: []wire.Check{{Name: "resolver_guard", Status: "fail", Details: map[string]any{
				"mode": "fallback", "reason": "fallback", "candidate": "198.51.100.53",
			}}},
		})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	}

	post(t)
	disp.mu.Lock()
	for _, got := range disp.calls {
		if got == state.Hard {
			t.Fatalf("first resolver_guard fail is soft in the FSM, got hard: calls=%v", disp.calls)
		}
	}
	disp.mu.Unlock()

	post(t)
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if len(disp.calls) == 0 || disp.calls[len(disp.calls)-1] != state.Hard {
		t.Fatalf("second resolver_guard fail should become hard; calls=%v", disp.calls)
	}
}

// Одна удачная проба после HARD -- ещё не восстановление: no_live_fallback
// снимается без cooldown, и перемежающийся сервер иначе давал бы пару
// HARD/восстановление каждые несколько отчётов.
func TestReportResolverGuardRecoversOnlyAfterSecondOK(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3333333333333333333333333333333333333333333333333333333333333333"
	_, _ = d.Users().Insert("guardbox2", tok, "198.51.100.10", "awg0")

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  disp,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
		AlertPolicy: AlertPolicy{NoisyFailThreshold: 6, NoisyRecoveryThreshold: 3},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	baseTS := time.Now().UTC()
	seq := 0
	post := func(t *testing.T, status string, details map[string]any) {
		t.Helper()
		seq++
		body, _ := json.Marshal(wire.Report{
			Timestamp:    baseTS.Add(time.Duration(seq) * time.Second),
			AgentVersion: "test",
			Checks:       []wire.Check{{Name: "resolver_guard", Status: status, Details: details}},
		})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	}
	last := func() state.Kind {
		disp.mu.Lock()
		defer disp.mu.Unlock()
		if len(disp.calls) == 0 {
			return state.Noop
		}
		return disp.calls[len(disp.calls)-1]
	}

	noLive := map[string]any{"reason": "no_live_fallback"}
	primary := map[string]any{"mode": "primary"}
	post(t, "fail", noLive)
	post(t, "fail", noLive)
	if got := last(); got != state.Hard {
		t.Fatalf("setup: second fail should be hard, last=%v", got)
	}
	post(t, "ok", primary)
	if got := last(); got == state.Recovery {
		t.Fatalf("one ok after hard must not recover resolver_guard (flapping no_live_fallback)")
	}
	post(t, "ok", primary)
	if got := last(); got != state.Recovery {
		t.Fatalf("second ok after hard should recover, last=%v", got)
	}
}

func TestReportNoisyDNSUsesHigherFailThreshold(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "3131313131313131313131313131313131313131313131313131313131313131"
	_, _ = d.Users().Insert("dnsbox", tok, "1.1.1.1", "awg0")

	disp := &fakeDisp{db: d}
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
		AlertPolicy: AlertPolicy{
			NoisyFailThreshold:     6,
			NoisyRecoveryThreshold: 3,
		},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	baseTS := time.Now().UTC()
	var dnsSeq int
	postDNS := func(t *testing.T) {
		t.Helper()
		dnsSeq++
		body, _ := json.Marshal(wire.Report{
			Timestamp:    baseTS.Add(time.Duration(dnsSeq) * time.Second),
			AgentVersion: "test",
			Checks:       []wire.Check{{Name: "dns", Status: "fail"}},
		})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	}

	for i := 0; i < 5; i++ {
		postDNS(t)
	}
	disp.mu.Lock()
	for i, got := range disp.calls {
		if got == state.Hard {
			t.Fatalf("dns failure #%d became hard before noisy threshold: calls=%v", i+1, disp.calls)
		}
	}
	disp.mu.Unlock()

	postDNS(t)
	disp.mu.Lock()
	defer disp.mu.Unlock()
	if got := disp.calls[len(disp.calls)-1]; got != state.Hard {
		t.Fatalf("6th dns failure should become hard; last=%v calls=%v", got, disp.calls)
	}
}

type fakeCmdSink struct {
	mu            sync.Mutex
	dequeueRet    *wire.Command
	dequeueWaitMs int
	results       []wire.CommandResult
	resultErr     error
	originRef     *cmdpkg.MessageRef // returned once by ConsumeOriginRef when set
	commands      map[string]wire.Command
	enqueued      []wire.Command
	enqueuedUsers []int64
	// active изображает занятость очереди: действие, по которому команда
	// уже в работе, HasActiveCommand подтвердит.
	active map[string]bool
}

func (f *fakeCmdSink) HasActiveCommand(userID int64, action string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[action]
}

func (f *fakeCmdSink) snapshotEnqueued() []wire.Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wire.Command, len(f.enqueued))
	copy(out, f.enqueued)
	return out
}

func (f *fakeCmdSink) Dequeue(ctx context.Context, userID int64, hold time.Duration) (*wire.Command, bool) {
	if f.dequeueWaitMs > 0 {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(time.Duration(f.dequeueWaitMs) * time.Millisecond):
		}
	}
	if f.dequeueRet == nil {
		return nil, false
	}
	c := *f.dequeueRet
	return &c, true
}

func (f *fakeCmdSink) RecordResult(userID int64, r wire.CommandResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resultErr != nil {
		return f.resultErr
	}
	f.results = append(f.results, r)
	return nil
}

func (f *fakeCmdSink) ConsumeOriginRef(userID int64, cmdID string) (cmdpkg.MessageRef, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.originRef == nil {
		return cmdpkg.MessageRef{}, false
	}
	r := *f.originRef
	f.originRef = nil // consume
	return r, true
}

func (f *fakeCmdSink) CommandByID(userID int64, cmdID string) (wire.Command, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commands == nil {
		return wire.Command{}, false
	}
	c, ok := f.commands[cmdID]
	return c, ok
}

func (f *fakeCmdSink) Enqueue(userID int64, cmd wire.Command) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueuedUsers = append(f.enqueuedUsers, userID)
	f.enqueued = append(f.enqueued, cmd)
	return nil
}
func (f *fakeCmdSink) DropPending(int64, string) []wire.Command {
	return nil
}
func (f *fakeCmdSink) AwaitResult(ctx context.Context, userID int64, id string, timeout time.Duration) (*wire.CommandResult, bool) {
	return nil, false
}

type relayCapture struct {
	mu      sync.Mutex
	chunks  []string
	chatID  int64
	thread  *int64
	replyTo int64
	action  string
}

func (rc *relayCapture) NotifyCommandResult(ctx context.Context, ref cmdpkg.MessageRef, action string, result wire.CommandResult, userID int64, maxChars int) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.chatID = ref.ChatID
	rc.thread = ref.ThreadID
	rc.replyTo = ref.MessageID
	rc.action = action
	rc.chunks = append(rc.chunks, alerts.FormatCommandResult(action, result, maxChars)...)
	return nil
}

type relaySnapshot struct {
	chunks  []string
	chatID  int64
	thread  *int64
	replyTo int64
	action  string
}

func (rc *relayCapture) snapshot() relaySnapshot {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	out := relaySnapshot{
		chatID:  rc.chatID,
		thread:  rc.thread,
		replyTo: rc.replyTo,
		action:  rc.action,
	}
	out.chunks = append(out.chunks, rc.chunks...)
	return out
}

func TestCmdResultRelayedToTG(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	tid := int64(11)
	sink := &fakeCmdSink{
		originRef: &cmdpkg.MessageRef{
			ChatID: -100, MessageID: 42, ThreadID: &tid, Action: "diag_now",
		},
	}
	rc := &relayCapture{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		TGNotifier:  rc,
		UI:          UIConfig{DiagMaxChars: 3500},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok", Output: "diagnostics: all green"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Relay is async (goroutine). Poll briefly.
	waitForRelay(t, func() int { return len(rc.snapshot().chunks) }, 1, 500*time.Millisecond)

	got := rc.snapshot()
	if len(got.chunks) != 1 {
		t.Fatalf("want 1 relay chunk, got %d", len(got.chunks))
	}
	if got.chatID != -100 || got.replyTo != 42 || got.thread == nil || *got.thread != 11 {
		t.Errorf("ref mis-routed: chatID=%d reply=%d thread=%v", got.chatID, got.replyTo, got.thread)
	}
	if got.action != "diag_now" {
		t.Errorf("action mismatch: %q", got.action)
	}
	if !strings.Contains(got.chunks[0], "diagnostics: all green") {
		t.Errorf("output missing: %s", got.chunks[0])
	}
}

func TestCmdResultNoRelayWhenNotifierNil(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f1f"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{
		originRef: &cmdpkg.MessageRef{ChatID: -100, MessageID: 42, Action: "diag_now"},
	}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		// TGNotifier intentionally nil
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	// No assertion on relay — just must not panic / 500.
}

func TestCmdGet_ReturnsQueuedCommand(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	want := &wire.Command{ID: "abc", Action: "diag_now", IssuedAt: time.Now().UTC()}
	sink := &fakeCmdSink{dequeueRet: want}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd?wait=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var got wire.Command
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "abc" || got.Action != "diag_now" {
		t.Errorf("got %+v", got)
	}
}

func TestCmdGet_204WhenIdle(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b0b"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{} // dequeueRet=nil → no command
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd?wait=1", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestCmdGet_RequiresAuth(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: &fakeCmdSink{},
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/v1/cmd", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestCmdResult_PostRecords(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c0c"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "ok", Output: "done", DurationMs: 42})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.results) != 1 {
		t.Fatalf("RecordResult called %d times", len(sink.results))
	}
	if sink.results[0].ID != "abc" || sink.results[0].Status != "ok" || sink.results[0].DurationMs != 42 {
		t.Errorf("got %+v", sink.results[0])
	}
}

func TestCmdResult_RejectsBadJSON(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d0d"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

type fakeRoutesNotifier struct {
	mu     sync.Mutex
	called int
}

func (f *fakeRoutesNotifier) NotifyCommandResult(_ context.Context, _ cmdpkg.MessageRef, _ wire.CommandResult, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called++
	return nil
}

func TestCmdResult_DispatchesRoutesNotifier(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	rn := &fakeRoutesNotifier{}
	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		Dispatcher:     &fakeDisp{},
		CommandSink:    sink,
		TGNotifier:     rc,
		RoutesNotifier: rn,
		Thresholds:     state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "x", Status: "ok", Output: `{"tunnels":[]}`, DurationMs: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	// Goroutine dispatch is async — poll briefly.
	rnCalled := waitForRelay(t, func() int {
		rn.mu.Lock()
		defer rn.mu.Unlock()
		return rn.called
	}, 1, 500*time.Millisecond)
	if rnCalled != 1 {
		t.Errorf("RoutesNotifier called %d times, want 1", rnCalled)
	}

	snap := rc.snapshot()
	if len(snap.chunks) != 0 {
		t.Errorf("generic relay called %d chunk(s) for route_status, want 0", len(snap.chunks))
	}
}

func TestCmdResult_AcceptsLargeRouteSnapshot(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01aa01"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")

	rn := &fakeRoutesNotifier{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "route_status", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		Dispatcher:     &fakeDisp{},
		CommandSink:    sink,
		RoutesNotifier: rn,
		Thresholds:     state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	largeSnapshot := `{"tunnels":[],"rules":[{"id":"hr:large","name":"large","kind":"dns","targets":["` +
		strings.Repeat("example.com,", 3000) + `"]}]}`
	body, _ := json.Marshal(wire.CommandResult{ID: "x", Status: "ok", Output: largeSnapshot, DurationMs: 1})
	if len(body) <= 16*1024 {
		t.Fatalf("test payload=%d, want larger than old 16 KiB limit", len(body))
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("large route_status result status: %d", resp.StatusCode)
	}
}

// TestCmdResult_DefaultRelayRespectsPoolBound is the pool-bound proof for
// the generic TGNotifier ("default" case) relay — the highest-traffic of the
// cmd-result relay sites, since it fires for any action without a
// specialized notifier.
func TestCmdResult_DefaultRelayRespectsPoolBound(t *testing.T) {
	waitRelayPoolEmpty(t)
	fillDeps := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ShutdownCtx: context.Background()}
	release := make(chan struct{})
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	for i := 0; i < relayConcurrencyLimit; i++ {
		spawnRelay(fillDeps, "fill", func(ctx context.Context) { <-release })
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	tok := "be01be01be01be01be01be01be01be01be01be01be01be01be01be01be01be01"
	if _, err := d.Users().Insert("vasya", tok, "1.1.1.1", "awg0"); err != nil {
		t.Fatal(err)
	}
	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "diag_now", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		TGNotifier:  rc,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "d1", Status: "ok", Output: "diag ok", DurationMs: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	time.Sleep(150 * time.Millisecond)
	if chunks := rc.snapshot().chunks; len(chunks) != 0 {
		t.Fatalf("default TGNotifier relayed %d chunk(s) while relay pool was saturated; want 0 (dropped)", len(chunks))
	}

	closeRelease()
	waitRelayPoolEmpty(t)
}

// Панели обслуживания в боте нет: итог service_restart (кнопки HR-Neo в
// панели маршрутов) уходит общим TGNotifier.
func TestCmdResult_ServiceRestartRelaysThroughTGNotifier(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02bb02"
	d.Users().Insert("vasya", tok, "198.51.100.20", "awg0")

	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "service_restart", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		TGNotifier:  rc,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "m1", Status: "ok", Output: "hrneo restart sent", DurationMs: 1})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := waitForRelay(t, func() int { return len(rc.snapshot().chunks) }, 1, 500*time.Millisecond); got != 1 {
		t.Errorf("TGNotifier: expected 1 chunk, got %d", got)
	}
}

// TestCmdResult_AcceptsUnknownStatus verifies forward-compat: a status not in
// wire.validCommandResultStatuses is logged but accepted (200), so a future
// agent emitting "partial"/"rate_limited"/etc. doesn't lose its result during
// a rolling fleet upgrade. Empty status is still rejected (400) — that's a
// real client bug, not schema evolution.
func TestCmdResult_AcceptsUnknownStatus(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e0e"
	d.Users().Insert("vasya", tok, "1.1.1.1", "awg0")
	sink := &fakeCmdSink{}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Unknown but non-empty status must be accepted (forward-compat).
	body, _ := json.Marshal(wire.CommandResult{ID: "abc", Status: "weird"})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for unknown status (forward-compat), got %d", resp.StatusCode)
	}

	// Empty status must still be rejected. 422 = parsed correctly, semantic
	// violation of required field (API-03).
	body, _ = json.Marshal(wire.CommandResult{ID: "abc2", Status: ""})
	req, _ = http.NewRequest("POST", srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422 for empty status, got %d", resp.StatusCode)
	}
}

func TestCmdResult_OpkgResultRelaysThroughTGNotifier(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	tok := "dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01dd01"
	d.Users().Insert("vasya", tok, "198.51.100.21", "awg0")

	rc := &relayCapture{}
	sink := &fakeCmdSink{originRef: &cmdpkg.MessageRef{Action: "opkg_upgrade", ChatID: 1, MessageID: 2}}
	mux := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  &fakeDisp{},
		CommandSink: sink,
		TGNotifier:  rc,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(wire.CommandResult{ID: "op2", Status: "ok", Output: "✅ обновлено"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	got := waitForRelay(t, func() int { return len(rc.snapshot().chunks) }, 1, 500*time.Millisecond)
	if got != 1 {
		t.Errorf("TGNotifier fallback: expected 1 chunk, got %d", got)
	}
}

// Первая неудача -- не повод будить людей и снимать отметку: причина
// запоминается, досылка на контакте попробует снова.
func TestCmdResult_FirstSelfUpdateFailureKeepsPendingQuietly(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-self-update-fail.db"))
	defer d.Close()
	tok := "eded00eded00eded00eded00eded00eded00eded00eded00eded00eded00eded"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().MarkPendingDeploy(uid, "v0.13.0-rc53", "2026-06-15T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Users().IncrementPendingAttempts(uid, "v0.13.0-rc53"); err != nil {
		t.Fatal(err)
	}
	deploy := &fakeDeployNotifier{}
	sink := &fakeCmdSink{commands: map[string]wire.Command{
		"cmd1": {ID: "cmd1", Action: "self_update", Args: map[string]any{"version": "v0.13.0-rc53"}},
	}}
	h := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		CommandSink:    sink,
		DeployNotifier: deploy,
	})

	body, _ := json.Marshal(wire.CommandResult{ID: "cmd1", Status: "err", Output: "download checksums.txt: HTTP 502"})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Без сна: recordPendingDeployFailure на первой неудаче вызывается
	// синхронно из cmdResultHandler (giveUpPendingDeploy, который единственный
	// шлёт уведомление, срабатывает только на исчерпанных попытках) -- к
	// возврату ServeHTTP всё уже случилось или не случится вовсе (B3).
	if calls := deploy.snapshot(); len(calls) != 0 {
		t.Fatalf("первая неудача не должна писать людям: %+v", calls)
	}
	st, err := d.Users().PendingDeploy(uid)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "v0.13.0-rc53" || !strings.Contains(st.LastError, "HTTP 502") {
		t.Fatalf("отметка и причина обязаны остаться: %+v", st)
	}
}

func TestCmdResult_ThirdSelfUpdateFailureClearsPendingWithoutDeployNotifier(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-self-update-fail-no-notifier.db"))
	defer d.Close()
	tok := "eded01eded01eded01eded01eded01eded01eded01eded01eded01eded01eded"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().MarkPendingDeploy(uid, "v0.13.0-rc53", "2026-06-15T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := d.Users().IncrementPendingAttempts(uid, "v0.13.0-rc53"); err != nil {
			t.Fatal(err)
		}
	}
	sink := &fakeCmdSink{commands: map[string]wire.Command{
		"cmd1": {ID: "cmd1", Action: "self_update", Args: map[string]any{"version": "v0.13.0-rc53"}},
	}}
	h := NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		CommandSink: sink,
	})

	body, _ := json.Marshal(wire.CommandResult{ID: "cmd1", Status: "err", Output: "download checksums.txt: HTTP 502"})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion != nil || u.PendingSince != nil {
		t.Fatalf("третья неудача обязана снять отметку и без уведомителя: version=%v since=%v", u.PendingVersion, u.PendingSince)
	}
}

// Протухшая команда -- выброшенный носитель, а не отказ от намерения:
// отметка остаётся, досылка на контакте положит новую.
func TestExpiredSelfUpdateKeepsPendingDeploy(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-self-update-expired.db"))
	defer d.Close()
	tok := "eded22eded22eded22eded22eded22eded22eded22eded22eded22eded22eded"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().MarkPendingDeploy(uid, "v0.13.0-rc200", "2026-06-15T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	q := cmdpkg.New()
	AttachDeployExpiryHandler(q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	expired := wire.Command{
		ID:        "cmd-expired",
		Action:    "self_update",
		Args:      map[string]any{"version": "v0.13.0-rc200"},
		IssuedAt:  time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(-time.Second),
	}
	if err := q.Enqueue(uid, expired); err != nil {
		t.Fatalf("enqueue expired self_update: %v", err)
	}
	if got, ok := q.Dequeue(context.Background(), uid, 10*time.Millisecond); ok || got != nil {
		t.Fatalf("expired self_update must not be issued, got %+v ok=%v", got, ok)
	}
	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion == nil || *u.PendingVersion != "v0.13.0-rc200" {
		t.Fatalf("протухшая команда сняла отметку: version=%v", u.PendingVersion)
	}
}

// Вытеснение прежней команды новой -- тоже не отказ от намерения.
func TestSupersededSelfUpdateKeepsPendingDeploy(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-self-update-superseded.db"))
	defer d.Close()
	tok := "eded33eded33eded33eded33eded33eded33eded33eded33eded33eded33eded"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().MarkPendingDeploy(uid, "v0.13.0-rc200", "2026-06-15T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	q := cmdpkg.New()
	AttachDeployExpiryHandler(q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, id := range []string{"cmd-old", "cmd-new"} {
		if err := q.Enqueue(uid, wire.Command{
			ID:       id,
			Action:   "self_update",
			Args:     map[string]any{"version": "v0.13.0-rc200"},
			IssuedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	u, err := d.Users().GetByID(uid)
	if err != nil {
		t.Fatal(err)
	}
	if u.PendingVersion == nil || *u.PendingVersion != "v0.13.0-rc200" {
		t.Fatalf("вытесненная команда сняла отметку: version=%v", u.PendingVersion)
	}
}

func TestCmdResult_DuplicateSelfUpdateFailureDoesNotNotifyAgain(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-self-update-dup.db"))
	defer d.Close()
	tok := "eded11eded11eded11eded11eded11eded11eded11eded11eded11eded11eded"
	_, _ = d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	deploy := &fakeDeployNotifier{}
	sink := &fakeCmdSink{
		resultErr: cmdpkg.ErrDuplicateResult,
		commands: map[string]wire.Command{
			"cmd1": {
				ID:     "cmd1",
				Action: "self_update",
				Args:   map[string]any{"version": "v0.13.0-rc53"},
			},
		},
	}
	h := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		CommandSink:    sink,
		DeployNotifier: deploy,
	})

	body, _ := json.Marshal(wire.CommandResult{ID: "cmd1", Status: "err", Output: "download checksums.txt: HTTP 502"})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if calls := deploy.snapshot(); len(calls) != 0 {
		t.Fatalf("duplicate result must not notify deferred update again: %+v", calls)
	}
}

func TestCmdResult_UnissuedResultACKsWithoutNotify(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "cmd-unissued-result.db"))
	defer d.Close()
	tok := "eded11eded11eded11eded11eded11eded11eded11eded11eded11eded11eded"
	_, _ = d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	deploy := &fakeDeployNotifier{}
	sink := &fakeCmdSink{resultErr: cmdpkg.ErrUnissuedResult}
	h := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		CommandSink:    sink,
		DeployNotifier: deploy,
	})

	body, _ := json.Marshal(wire.CommandResult{ID: "cmd-after-restart", Status: "err", Output: "late result"})
	req := httptest.NewRequest(http.MethodPost, "/v1/cmd/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if calls := deploy.snapshot(); len(calls) != 0 {
		t.Fatalf("unissued stale result must not notify deferred update: %+v", calls)
	}
}

type fakeWakeNotifier struct {
	mu    sync.Mutex
	calls []wakeRec
}

type wakeRec struct {
	userID   int64
	nickname string
	checks   []wire.Check
}

func (f *fakeWakeNotifier) SendWake(_ context.Context, uid int64, nick string, checks []wire.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, wakeRec{uid, nick, append([]wire.Check(nil), checks...)})
	return nil
}

func (f *fakeWakeNotifier) snapshot() []wakeRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wakeRec, len(f.calls))
	copy(out, f.calls)
	return out
}

type fakeDeployNotifier struct {
	mu    sync.Mutex
	calls []deployRec
}

type deployRec struct {
	userID   int64
	nickname string
	target   string
	status   string
	output   string
}

func (f *fakeDeployNotifier) SendDeferredUpdate(ctx context.Context, userID int64, nickname, targetVersion, status, output string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, deployRec{userID, nickname, targetVersion, status, output})
	return nil
}

func (f *fakeDeployNotifier) snapshot() []deployRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]deployRec, len(f.calls))
	copy(out, f.calls)
	return out
}

// Версии роутера приезжают в каждом отчёте, а «было» помнит только база:
// без снимка сказать «вышло обновление» физически нечем, и кэш в памяти
// умирал с каждым рестартом бэкенда.
func TestReportWritesVersionSnapshot(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "v.db"))
	defer d.Close()
	tok := strings.Repeat("cd", 32)
	uid, _ := d.Users().Insert("router-v", tok, "198.51.100.10", "awg11")

	h := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
	})
	body, _ := json.Marshal(wire.Report{
		Timestamp: time.Now().UTC(), AgentVersion: "t",
		Checks: []wire.Check{{Name: "awg_manager", Status: "ok", Details: map[string]any{
			"version": "2.17.2", "firmware": "4.3.5", "keenetic_os": "KN-1811",
			"active_backend":        "kernel",
			"kernel_module_version": "1.0.0",
			"kernel_module_model":   "KN-1811",
			"kernel_module_loaded":  true,
		}}},
	})
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.AwgmgrVersion != "2.17.2" || row.KmodVersion != "1.0.0" {
		t.Errorf("снимок из отчёта не записан: %+v", row)
	}
	if row.Source != "report" {
		t.Errorf("source = %q", row.Source)
	}
	if row.FirmwareCurrent != "4.3.5" || row.KeeneticOS != "KN-1811" || row.AwgmgrBackend != "kernel" {
		t.Errorf("снимок неполон: %+v", row)
	}
	if row.KmodModel != "KN-1811" || row.KmodLoaded == nil || !*row.KmodLoaded {
		t.Errorf("модуль ядра не записан: %+v", row)
	}
}

// Отчёт старого агента без полей модуля ядра не ломает запись снимка.
func TestReportFromOldAgentStillWritesSnapshot(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "v.db"))
	defer d.Close()
	tok := strings.Repeat("ce", 32)
	uid, _ := d.Users().Insert("router-v", tok, "198.51.100.10", "awg11")

	h := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
	})
	body, _ := json.Marshal(wire.Report{
		Timestamp: time.Now().UTC(), AgentVersion: "t",
		Checks: []wire.Check{{Name: "awg_manager", Status: "ok", Details: map[string]any{
			"version": "2.17.2", "firmware": "4.3.5",
		}}},
	})
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.AwgmgrVersion != "2.17.2" {
		t.Errorf("снимок старого агента не записан: %+v", row)
	}
	if row.KmodLoaded != nil {
		t.Error("у старого агента kmod_loaded обязан остаться неизвестным")
	}
}

// Проверка упала -- в details лежит только адрес панели, версий там нет.
// Такой отчёт не имеет права стереть то, что мы уже знали.
func TestReportFailedAwgManagerCheckKeepsSnapshot(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "v.db"))
	defer d.Close()
	tok := strings.Repeat("cf", 32)
	uid, _ := d.Users().Insert("router-v", tok, "198.51.100.10", "awg11")
	if err := d.RouterVersions().Upsert(uid, db.RouterVersionSnapshot{
		AwgmgrVersion: "2.17.2", KmodVersion: "1.0.0", Source: "report",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}

	h := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
	})
	body, _ := json.Marshal(wire.Report{
		Timestamp: time.Now().UTC(), AgentVersion: "t",
		Checks: []wire.Check{{Name: "awg_manager", Status: "fail", Details: map[string]any{
			"base_url": "http://127.0.0.1:2222",
		}}},
	})
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	row, err := d.RouterVersions().Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.AwgmgrVersion != "2.17.2" || row.KmodVersion != "1.0.0" {
		t.Errorf("упавшая проверка стёрла снимок: %+v", row)
	}
	if row.PrevAwgmgrVersion != "" {
		t.Errorf("упавшая проверка сдвинула историю: prev=%q", row.PrevAwgmgrVersion)
	}
	// Сохранность значений держит и слияние в SQL, поэтому она гейт не
	// сторожит вовсе. Сторожит вот это: Upsert двигает updated_at
	// БЕЗУСЛОВНО, и без гейта упавшая проверка обновила бы «когда
	// смотрели», не принеся ни одной версии. Экран задачи 4 печатает это
	// время вслух.
	if !row.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("упавшая проверка сдвинула «когда смотрели»: %v -> %v", before.UpdatedAt, row.UpdatedAt)
	}
	// И прямо о предмете гейта, без похода через HTTP.
	if _, ok := versionSnapshotFromReport(`{"base_url":"http://127.0.0.1:2222"}`); ok {
		t.Error("гейт пропустил details, в которых нет ни одной версии")
	}
}

func TestHandleReport_MobileResumed_TriggersWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "abab00abab00abab00abab00abab00abab00abab00abab00abab00abab00abab"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	lastSeen := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, lastSeen, uid); err != nil {
		t.Fatal(err)
	}

	wake := &fakeWakeNotifier{}
	disp := &fakeDisp{}
	resumer := &fakeResumer{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   disp,
		Resumer:      resumer,
		WakeNotifier: wake,
	})

	body := []byte(`{"ts":"2026-05-15T14:31:00Z","agent_version":"t","resumed":true,"checks":[{"name":"tunnels","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	// SendWake is fired in a goroutine; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(wake.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := wake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 wake call, got %d", len(calls))
	}
	if calls[0].userID != uid || calls[0].nickname != "client-h" {
		t.Errorf("call mismatch: %+v", calls[0])
	}
}

func TestHandleReport_MobileNoisyResumedInsideWakeThreshold_NoWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "abab11abab11abab11abab11abab11abab11abab11abab11abab11abab11abab"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	lastSeen := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, lastSeen, uid); err != nil {
		t.Fatal(err)
	}

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              d,
		Dispatcher:      &fakeDisp{},
		Resumer:         &fakeResumer{},
		WakeNotifier:    wake,
		MobileWakeAfter: 30 * time.Minute,
	})
	body := []byte(`{"ts":"2026-05-15T14:06:00Z","agent_version":"t","resumed":true,"checks":[{"name":"tunnels","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(wake.snapshot()); got != 0 {
		t.Fatalf("short mobile gap with noisy resumed must not wake, got %d calls", got)
	}
}

func TestHandleReport_MobileFreshAfterSleep_TriggersWakeCardWithoutResumed(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "dada00dada00dada00dada00dada00dada00dada00dada00dada00dada00dada"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	lastSeen := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, lastSeen, uid); err != nil {
		t.Fatal(err)
	}

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              d,
		Dispatcher:      &fakeDisp{},
		Resumer:         &fakeResumer{},
		WakeNotifier:    wake,
		MobileWakeAfter: 5 * time.Minute,
	})
	body := []byte(`{"ts":"2026-05-15T14:07:00Z","agent_version":"t","checks":[{"name":"tunnels","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(wake.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := wake.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 wake call after stale heartbeat gap, got %d", len(calls))
	}
	if calls[0].userID != uid || calls[0].nickname != "client-h" {
		t.Errorf("call mismatch: %+v", calls[0])
	}
}

func TestHandleReport_ClearsHardForTunnelMissingFromFreshInventory(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "acac00acac00acac00acac00acac00acac00acac00acac00acac00acac00acac"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	hs := time.Date(2026, 5, 15, 13, 0, 0, 0, time.UTC)
	if err := d.State().Save(uid, "tunnel_awg11", db.IncidentState{
		UserID: uid, CheckName: "tunnel_awg11", CurrentStatus: "hard",
		ConsecutiveFails: 12, HardSince: &hs, LastAlertAt: &hs,
	}); err != nil {
		t.Fatal(err)
	}

	disp := &fakeDisp{db: d}
	h := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: disp,
		Thresholds: state.Thresholds{Fail: 2, Recovery: 2},
	})
	body := []byte(`{"ts":"2026-05-15T14:00:00Z","agent_version":"t","checks":[` +
		`{"name":"tunnels","status":"ok","details":{"tunnel_count":2}},` +
		`{"name":"tunnel_awg10","status":"ok"},` +
		`{"name":"tunnel_awg12","status":"ok"},` +
		`{"name":"agent_heartbeat","status":"ok"}` +
		`]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	got, err := d.State().Get(uid, "tunnel_awg11")
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentStatus != "ok" || got.ConsecutiveFails != 0 || got.HardSince != nil {
		t.Fatalf("missing tunnel hard must be cleared after fresh inventory: %+v", got)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	foundRecovery := false
	for _, call := range disp.calls {
		if call == state.Recovery {
			foundRecovery = true
			break
		}
	}
	if !foundRecovery {
		t.Fatalf("missing tunnel hard must go through dispatcher recovery, calls=%v", disp.calls)
	}
}

// guardReportHarness -- сервер отчётов и отправка отчёта с набором проверок.
func guardReportHarness(t *testing.T, tok, nick string) (*fakeDisp, func(checks ...wire.Check), func(stale bool, checks ...wire.Check), *db.DB) {
	t.Helper()
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	t.Cleanup(func() { d.Close() })
	_, _ = d.Users().Insert(nick, tok, "198.51.100.11", "awg0")
	disp := &fakeDisp{db: d}
	srv := httptest.NewServer(NewMux(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:          d,
		Dispatcher:  disp,
		Thresholds:  state.Thresholds{Fail: 3, Recovery: 2},
		AlertPolicy: AlertPolicy{NoisyFailThreshold: 6, NoisyRecoveryThreshold: 3},
	}))
	t.Cleanup(srv.Close)
	baseTS := time.Now().UTC()
	seq := 0
	send := func(stale bool, checks ...wire.Check) {
		t.Helper()
		seq++
		ts := baseTS.Add(time.Duration(seq) * time.Second)
		if stale {
			ts = baseTS.Add(-24 * time.Hour)
		}
		body, _ := json.Marshal(wire.Report{Timestamp: ts, AgentVersion: "test", Checks: checks})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post := func(checks ...wire.Check) { send(false, checks...) }
	return disp, post, send, d
}

var heartbeatCheck = wire.Check{Name: "agent_heartbeat", Status: "ok"}

func guardFail() wire.Check {
	return wire.Check{Name: "resolver_guard", Status: "fail", Details: map[string]any{"mode": "fallback", "reason": "fallback"}}
}

// Сторож выключили правкой файла посреди аварии: проверка просто пропала из
// отчёта. Без закрытия realert напоминал бы о запасных вечно.
func TestReportClosesResolverGuardHardWhenTheCheckIsGone(t *testing.T) {
	disp, post, _, d := guardReportHarness(t, "3434343434343434343434343434343434343434343434343434343434343434", "guardgone")
	post(heartbeatCheck, guardFail())
	post(heartbeatCheck, guardFail())
	uid := mustUserID(t, d, "guardgone")
	if st, _ := d.State().Get(uid, "resolver_guard"); st.CurrentStatus != "hard" {
		t.Fatalf("setup: want hard, got %q", st.CurrentStatus)
	}
	post(heartbeatCheck, wire.Check{Name: "dns", Status: "ok"})
	if st, _ := d.State().Get(uid, "resolver_guard"); st.CurrentStatus != "ok" {
		t.Fatalf("HARD сторожа не закрыт: %q", st.CurrentStatus)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	last := disp.checks[len(disp.checks)-1]
	if disp.calls[len(disp.calls)-1] != state.Recovery || last.Name != "resolver_guard" || last.Details["reason"] != "watchdog_off" {
		t.Fatalf("want Recovery resolver_guard reason=watchdog_off, got %v %#v", disp.calls, last)
	}
}

// Несвежий отчёт и отчёт без heartbeat (не от агента целиком) ничего не закрывают.
func TestReportKeepsResolverGuardHardOnStaleOrPartialReport(t *testing.T) {
	_, post, send, d := guardReportHarness(t, "3535353535353535353535353535353535353535353535353535353535353535", "guardkeep")
	post(heartbeatCheck, guardFail())
	post(heartbeatCheck, guardFail())
	uid := mustUserID(t, d, "guardkeep")
	send(true, heartbeatCheck)
	post(wire.Check{Name: "dns", Status: "ok"})
	if st, _ := d.State().Get(uid, "resolver_guard"); st.CurrentStatus != "hard" {
		t.Fatalf("HARD закрыт несвежим или неполным отчётом: %q", st.CurrentStatus)
	}
}

// «Ещё не прочитал настройки» -- не «здоров»: два таких ok подряд закрывали
// инцидент, а когда чтение проходило, открывался новый с нуля.
func TestReportResolverGuardNotReadyNeitherRecoversNorCloses(t *testing.T) {
	disp, post, _, d := guardReportHarness(t, "3636363636363636363636363636363636363636363636363636363636363636", "guardunread")
	post(heartbeatCheck, guardFail())
	post(heartbeatCheck, guardFail())
	uid := mustUserID(t, d, "guardunread")
	before, _ := d.State().Get(uid, "resolver_guard")
	notReady := wire.Check{Name: "resolver_guard", Status: "ok", Details: map[string]any{"mode": "primary", "ready": false}}
	for i := 0; i < 3; i++ {
		post(heartbeatCheck, notReady)
	}
	after, _ := d.State().Get(uid, "resolver_guard")
	if after.CurrentStatus != "hard" || after.ConsecutiveOKs != before.ConsecutiveOKs {
		t.Fatalf("ready:false сдвинул автомат: before %+v after %+v", before, after)
	}
	disp.mu.Lock()
	defer disp.mu.Unlock()
	for _, k := range disp.calls {
		if k == state.Recovery {
			t.Fatalf("ready:false дал восстановление: %v", disp.calls)
		}
	}
}

func mustUserID(t *testing.T, d *db.DB, nick string) int64 {
	t.Helper()
	u, err := d.Users().GetByNickname(nick)
	if err != nil || u == nil {
		t.Fatalf("user %s: %v", nick, err)
	}
	return u.ID
}

type contextWakeNotifier struct {
	mu     sync.Mutex
	ctxErr error
}

func (f *contextWakeNotifier) SendWake(ctx context.Context, _ int64, _ string, _ []wire.Check) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxErr = ctx.Err()
	return nil
}

func (f *contextWakeNotifier) snapshot() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxErr
}

func TestHandleReport_WakeNotifierUsesShutdownContext(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "eded11eded11eded11eded11eded11eded11eded11eded11eded11eded11eded"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	lastSeen := time.Date(2026, 5, 15, 14, 0, 0, 0, time.UTC)
	if _, err := d.SQL().Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, lastSeen, uid); err != nil {
		t.Fatal(err)
	}

	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	wake := &contextWakeNotifier{}
	h := NewMux(Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              d,
		Dispatcher:      &fakeDisp{},
		WakeNotifier:    wake,
		ShutdownCtx:     shutdownCtx,
		MobileWakeAfter: 5 * time.Minute,
	})
	body := []byte(`{"ts":"2026-05-15T14:07:00Z","agent_version":"t","checks":[{"name":"tunnels","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wake.snapshot() != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := wake.snapshot(); got != context.Canceled {
		t.Fatalf("wake notifier context err=%v, want context.Canceled", got)
	}
}

func TestHandleReport_StaticResumed_NoWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc00bcbc"
	d.Users().Insert("homestat", tok, "1.1.1.1", "awg0")

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   &fakeDisp{},
		Resumer:      &fakeResumer{},
		WakeNotifier: wake,
	})
	body := []byte(`{"ts":"2026-05-15T14:30:00Z","agent_version":"t","resumed":true,"checks":[]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(wake.snapshot()); got != 0 {
		t.Errorf("static user must not fire wake card, got %d", got)
	}
}

func TestHandleReport_MobileNotResumed_NoWakeCard(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd00cdcd"
	d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)

	wake := &fakeWakeNotifier{}
	h := NewMux(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:           d,
		Dispatcher:   &fakeDisp{},
		Resumer:      &fakeResumer{},
		WakeNotifier: wake,
	})
	body := []byte(`{"ts":"2026-05-15T14:30:00Z","agent_version":"t","resumed":false,"checks":[]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(wake.snapshot()); got != 0 {
		t.Errorf("non-resumed mobile must not fire wake card, got %d", got)
	}
}

func TestHandleReport_PendingSelfUpdateSuccess_NotifiesTopic(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "h.db"))
	defer d.Close()
	tok := "efef00efef00efef00efef00efef00efef00efef00efef00efef00efef00efef"
	uid, _ := d.Users().InsertWithKind("client-h", tok, "1.1.1.1", "nwg0", db.KindMobile)
	if err := d.Users().UpdateDeployInfo("client-h", db.DeployInfo{
		LastDeployedVersion: "v0.13.0-rc52",
		PendingVersion:      "v0.13.0-rc53",
		PendingSince:        "2026-05-15T14:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	deploy := &fakeDeployNotifier{}
	h := NewMux(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d,
		Dispatcher:     &fakeDisp{},
		DeployNotifier: deploy,
	})
	body := []byte(`{"ts":"2026-05-15T14:07:00Z","agent_version":"v0.13.0-rc53","checks":[{"name":"agent_heartbeat","status":"ok"}]}`)
	req := httptest.NewRequest("POST", "/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(deploy.snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := deploy.snapshot()
	if len(calls) != 1 {
		t.Fatalf("want 1 deploy success notification, got %d", len(calls))
	}
	if calls[0].userID != uid || calls[0].nickname != "client-h" || calls[0].target != "v0.13.0-rc53" || calls[0].status != "ok" {
		t.Fatalf("call mismatch: %+v", calls[0])
	}
}

func TestReportRejectsTooLarge(t *testing.T) {
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	defer d.Close()
	mux := NewMux(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:         d,
		Dispatcher: &fakeDisp{},
		Thresholds: state.Thresholds{Fail: 3, Recovery: 2},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	huge := bytes.Repeat([]byte("A"), 80*1024)
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(huge))
	req.Header.Set("Authorization", "Bearer x")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

// hardReportEnv поднимает бэкенд, у которого проверка вот-вот уйдёт в hard:
// два провала уже записаны, третий придёт отчётом.
func hardReportEnv(t *testing.T, checkName string, start func(linkrepair.StartReq) (string, error)) (*httptest.Server, string) {
	t.Helper()
	d, _ := db.Open(filepath.Join(t.TempDir(), "t.db"))
	t.Cleanup(func() { _ = d.Close() })
	tok := "3030303030303030303030303030303030303030303030303030303030303030"
	uid, _ := d.Users().Insert("роутер", tok, "198.51.100.10", "awg0")
	// Два провала уже позади, и состояние именно fail: из ok счётчик
	// начинается заново, третий провал дал бы Soft, а не Hard.
	if err := d.State().Save(uid, checkName, db.IncidentState{
		CurrentStatus: "fail", ConsecutiveFails: 2,
	}); err != nil {
		t.Fatal(err)
	}
	mux := NewMux(Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:              d,
		Dispatcher:      &fakeDisp{db: d},
		Thresholds:      state.Thresholds{Fail: 3, Recovery: 2},
		StartLinkRepair: start,
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, tok
}

func postFailingCheck(t *testing.T, srv *httptest.Server, tok, checkName string) {
	t.Helper()
	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC().Truncate(time.Second),
		AgentVersion: "v0.19.7",
		Checks:       []wire.Check{{Name: checkName, Status: "fail"}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

// Переход в hard по упавшему туннелю запускает починку сам.
func TestHardTransition_StartsAutoRepair(t *testing.T) {
	var mu sync.Mutex
	got := linkrepair.StartReq{}
	srv, tok := hardReportEnv(t, "tunnel_awg12", func(req linkrepair.StartReq) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		got = req
		return "job-1", nil
	})
	postFailingCheck(t, srv, tok, "tunnel_awg12")

	mu.Lock()
	defer mu.Unlock()
	if got.CheckName != "tunnel_awg12" {
		t.Fatalf("починка не запустилась, CheckName=%q", got.CheckName)
	}
	if !got.Auto {
		t.Fatal("сторож обязан звать починку как автозапуск: у неё свои тормоза")
	}
	if got.AgentVersion != "v0.19.7" {
		t.Fatalf("версия агента не доехала: %q", got.AgentVersion)
	}
}

// Автозапуск держит в руках саму проверку, а в ней -- имя VPN-туннеля.
// Починке оно нужно, когда снимок от роутера не придёт: иначе владелец
// прочтёт в личке идентификатор «awg12».
func TestHardTransition_PassesTunnelName(t *testing.T) {
	var mu sync.Mutex
	got := linkrepair.StartReq{}
	srv, tok := hardReportEnv(t, "tunnel_awg12", func(req linkrepair.StartReq) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		got = req
		return "job-1", nil
	})
	body, _ := json.Marshal(wire.Report{
		Timestamp:    time.Now().UTC().Truncate(time.Second),
		AgentVersion: "v0.19.7",
		Checks: []wire.Check{{Name: "tunnel_awg12", Status: "fail",
			Details: map[string]any{"tunnel_name": "Дача"}}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/v1/report", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if got.CheckName != "tunnel_awg12" {
		t.Fatalf("починка не запустилась, CheckName=%q", got.CheckName)
	}
	if got.TunnelName != "Дача" {
		t.Fatalf("имя VPN-туннеля не доехало до починки: %q", got.TunnelName)
	}
}

// Молчащий роутер и пропавший интернет починку не запускают: сценария нет,
// а дёргать движок впустую значит писать в журнал отказ на каждый провал.
func TestHardTransition_SkipsUnfixable(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv, tok := hardReportEnv(t, "external_reach", func(linkrepair.StartReq) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return "", nil
	})
	postFailingCheck(t, srv, tok, "external_reach")

	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("движок звали %d раз, должно быть 0", calls)
	}
}
