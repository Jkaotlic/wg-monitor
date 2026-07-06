// internal/backend/provision_routes_test.go
//
// Task 9 (wire routes, BaseCtx, store sweeper): provision_handler_test.go
// deliberately exercises the three provision/repair handlers against a
// private local ServeMux, NOT the shared NewMux/registerDashboardRoutes (see
// its own newProvisionTestHandler doc comment) — Task 8 explicitly left
// wiring those handlers into the REAL dashboard mux for this task. Nothing
// else in the test suite proves POST /v1/dashboard/provision,
// GET /v1/dashboard/provision/{job_id}, and
// POST /v1/dashboard/agents/{nickname}/repair are actually reachable from the
// server operators hit — that's what these tests pin.
package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/provision"
)

// newProvisionWiredMux builds a real backend mux (NewMux/registerDashboardRoutes)
// with the provision engine wired in the same shape cmd/backend/main.go
// constructs it in production: a real Store, a stub Relay (so no test spawns
// a live python3/awgm-relay.py process), and LastSeen. LastSeen reuses
// freshLastSeen (provision_handler_test.go, same package): a real
// DB-backed closure would never see a fresh report for these fake test
// nicknames, so run's post-relay VerifyOnline poll would only resolve after
// riding out the full 120s provisionVerifyBudget in real time (verifySleep is
// real time.Sleep) — on a background goroutine this test function doesn't
// wait for, that would leak a goroutine polling a soon-to-be-t.Cleanup-closed
// DB for up to two minutes past the end of the test. freshLastSeen already
// solves exactly this in provision_handler_test.go's own install/repair
// tests; the DB-backed LastSeen closure's actual read-the-report-column
// behavior is unit-tested in isolation by cmd/backend/provision_wiring_test.go
// instead, so nothing loses coverage by not re-proving it here too.
func newProvisionWiredMux(t *testing.T) (*db.DB, *provision.Store, http.Handler) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	store := provision.NewStore()
	deps := Deps{
		DB:             database,
		DashboardToken: "secret",
		PublicBaseURL:  "https://wgmon.example",
		Provision: provision.Deps{
			Store:   store,
			BaseCtx: context.Background(),
			Relay: func(_ context.Context, _ string, _ []byte, _ func(string)) (int, error) {
				return 0, nil
			},
			LastSeen: freshLastSeen,
			Now:      time.Now,
		},
	}
	return database, store, NewMux(deps)
}

func doDashboardJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestDashboardProvisionRegister_WiredThroughRealMux is the primary Task 9
// acceptance test: kind=register through the REAL dashboard mux must return
// 201 + a raw token, exactly like the standalone-handler test already proves
// against a private mux. Before routes are registered in
// registerDashboardRoutes, this 404s (Go's ServeMux default for an unmatched
// pattern) instead of ever reaching dashboardProvisionHandler.
func TestDashboardProvisionRegister_WiredThroughRealMux(t *testing.T) {
	_, _, h := newProvisionWiredMux(t)

	body := `{"kind":"register","nickname":"wiredrouter","agent_kind":"static","telegram_group":100,"thread_id":5,"awgm_url":"https://awg.example","awgm_auth":"apikey"}`
	rec := doDashboardJSON(t, h, http.MethodPost, "/v1/dashboard/provision", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/dashboard/provision (real mux): status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}
	var resp dashboardRegisterResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RawToken == "" {
		t.Fatal("raw_token missing — route reached the register handler but no token was minted/returned")
	}
	if resp.Nickname != "wiredrouter" {
		t.Errorf("nickname = %q, want wiredrouter", resp.Nickname)
	}
	if resp.JobID == "" {
		t.Error("job_id missing — the wired Store should let register create its instant-success job")
	}
}

// TestDashboardProvisionPoll_WiredThroughRealMux creates a real job via the
// register path (through the same real mux) and then polls it. A 200 with a
// matching job id can only happen if the poll route is BOTH registered AND
// reading from the exact same Store the register handler wrote to.
func TestDashboardProvisionPoll_WiredThroughRealMux(t *testing.T) {
	_, _, h := newProvisionWiredMux(t)

	createRec := doDashboardJSON(t, h, http.MethodPost, "/v1/dashboard/provision",
		`{"kind":"register","nickname":"pollwired","agent_kind":"static"}`)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("setup: register status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var created dashboardRegisterResp
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.JobID == "" {
		t.Fatal("setup: register did not return a job_id")
	}

	pollRec := doDashboardJSON(t, h, http.MethodGet, "/v1/dashboard/provision/"+created.JobID, "")
	if pollRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/dashboard/provision/{job_id} (real mux): status = %d, want 200, body=%s", pollRec.Code, pollRec.Body.String())
	}
	var job provision.Job
	if err := json.Unmarshal(pollRec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID != created.JobID {
		t.Errorf("job id = %q, want %q", job.ID, created.JobID)
	}
}

// TestDashboardRepair_WiredThroughRealMux drives a repoint repair through the
// REAL mux end-to-end: a 202 Accepted with a job id can only happen if the
// repair route is registered, the engine is constructed with a non-nil
// Store/BaseCtx/Relay (a nil collaborator here would panic the worker
// goroutine — see provision.Deps' own doc), and Start actually launched it.
// Waiting for the job to reach StateSuccess (waitForProvisionTerminal, from
// provision_handler_test.go) proves the whole async path end-to-end and
// leaves no worker goroutine running past this test.
func TestDashboardRepair_WiredThroughRealMux(t *testing.T) {
	database, store, h := newProvisionWiredMux(t)
	if _, err := database.Users().UpsertEnrollment("bronya", "tok-bronya-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("bronya", db.DeployInfo{AWGMURL: "https://awg.example"}); err != nil {
		t.Fatal(err)
	}

	rec := doDashboardJSON(t, h, http.MethodPost, "/v1/dashboard/agents/bronya/repair",
		`{"mode":"repoint","root_password":"rootpw","new_backend_url":"https://new.example"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/dashboard/agents/{nickname}/repair (real mux): status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JobID == "" {
		t.Fatal("job_id missing")
	}

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job state = %q, want success (hint=%q)", job.State, job.Hint)
	}
}

// TestDashboardProvisionRoutes_RequireDashboardAuth pins that all three
// routes sit behind dashAuth like every other dashboard endpoint — an
// unauthenticated request must never reach the handler.
func TestDashboardProvisionRoutes_RequireDashboardAuth(t *testing.T) {
	_, _, h := newProvisionWiredMux(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/dashboard/provision", strings.NewReader(`{"kind":"register","nickname":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /v1/dashboard/provision: status = %d, want 401", rec.Code)
	}

	pollReq := httptest.NewRequest(http.MethodGet, "/v1/dashboard/provision/whatever", nil)
	pollRec := httptest.NewRecorder()
	h.ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /v1/dashboard/provision/{job_id}: status = %d, want 401", pollRec.Code)
	}

	repairReq := httptest.NewRequest(http.MethodPost, "/v1/dashboard/agents/bronya/repair", strings.NewReader(`{"mode":"repoint","root_password":"x"}`))
	repairReq.Header.Set("Content-Type", "application/json")
	repairRec := httptest.NewRecorder()
	h.ServeHTTP(repairRec, repairReq)
	if repairRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST /v1/dashboard/agents/{nickname}/repair: status = %d, want 401", repairRec.Code)
	}
}
