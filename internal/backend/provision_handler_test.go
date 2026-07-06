package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// --- test doubles --------------------------------------------------------

// fakeProvisionRelay is a stub provision.RelayFunc: it captures the exact
// jobJSON bytes Start hands it (so tests can assert on the assembled
// awgmInstallJob/awgmReviveJob without a real router/python3/relay) and the
// context's deadline (to prove a positive budget was threaded through), then
// completes with a fixed (rc, err) — mirroring internal/backend/provision's
// own scriptedRelay test double (runner_test.go), reused here at the
// HTTP-handler layer per the task brief's "inject a stub engine" instruction.
// block, when non-nil, makes run() wait for either a send/close or ctx.Done()
// before returning — used to hold the single-flight lock open across a
// second Start call.
type fakeProvisionRelay struct {
	mu          sync.Mutex
	calls       int
	jobJSON     []byte
	relayPath   string
	hasDeadline bool
	deadline    time.Time
	rc          int
	err         error
	block       <-chan struct{}
}

func (f *fakeProvisionRelay) run(ctx context.Context, relayPath string, jobJSON []byte, _ func(string)) (int, error) {
	f.mu.Lock()
	f.calls++
	f.jobJSON = append([]byte(nil), jobJSON...)
	f.relayPath = relayPath
	if dl, ok := ctx.Deadline(); ok {
		f.hasDeadline = true
		f.deadline = dl
	}
	block := f.block
	rc := f.rc
	err := f.err
	f.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
	}
	return rc, err
}

func (f *fakeProvisionRelay) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProvisionRelay) capturedJobJSON() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobJSON
}

// deadlineUntil returns how far in the future the captured context's
// deadline was at call time, and whether a deadline was set at all.
func (f *fakeProvisionRelay) deadlineUntil() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.hasDeadline {
		return 0, false
	}
	return time.Until(f.deadline), true
}

// --- test harness ---------------------------------------------------------

// newProvisionTestHandler builds a temp-DB-backed Deps wired to a stub
// provision engine (Store + the given fake relay + the given LastSeen) and a
// minimal local ServeMux exposing exactly the three provision/repair routes
// this task produces — NOT the shared NewMux/registerDashboardRoutes, since
// Task 9 (not this one) wires these into the real dashboard mux.
func newProvisionTestHandler(t *testing.T, relay *fakeProvisionRelay, lastSeen provision.LastSeenFunc) (*db.DB, *provision.Store, http.Handler) {
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
			Store:    store,
			BaseCtx:  context.Background(),
			Relay:    relay.run,
			LastSeen: lastSeen,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/dashboard/provision", dashboardProvisionHandler(deps))
	mux.HandleFunc("GET /v1/dashboard/provision/{job_id}", dashboardProvisionPollHandler(deps))
	mux.HandleFunc("POST /v1/dashboard/agents/{nickname}/repair", dashboardRepairHandler(deps))
	return database, store, mux
}

func postProvisionJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// waitForProvisionTerminal polls store.Get(jobID) until the job leaves
// StateRunning or max elapses — mirrors provision package's own
// runner_test.go waitForTerminal helper (duplicated here since that one is
// unexported in a different package).
func waitForProvisionTerminal(t *testing.T, store *provision.Store, jobID string, max time.Duration) provision.Job {
	t.Helper()
	deadline := time.Now().Add(max)
	for {
		job, ok := store.Get(jobID)
		if !ok {
			t.Fatalf("job %s: not found in store", jobID)
		}
		if job.State != provision.StateRunning {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not reach a terminal state within %s (last: state=%s hint=%q)",
				jobID, max, job.State, job.Hint)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func freshLastSeen(_ string) (time.Time, bool) { return time.Now().Add(time.Hour), true }

func stubLatestVersion(t *testing.T, version string) {
	t.Helper()
	orig := lookupDashboardLatestVersion
	lookupDashboardLatestVersion = func(context.Context) (string, error) { return version, nil }
	t.Cleanup(func() { lookupDashboardLatestVersion = orig })
}

func stubVerifiedChecksums(t *testing.T, sums map[string]string) {
	t.Helper()
	orig := verifiedChecksumsFetcher
	verifiedChecksumsFetcher = func(_ context.Context, _ string, _ string) (map[string]string, error) {
		return sums, nil
	}
	t.Cleanup(func() { verifiedChecksumsFetcher = orig })
}

// --- register --------------------------------------------------------------

func TestDashboardProvisionRegister_MintsTokenNoRelayCall(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })

	body := `{"kind":"register","nickname":"newrouter","agent_kind":"static","telegram_group":100,"thread_id":5,"awgm_url":"https://awg.example","awgm_auth":"apikey"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp dashboardRegisterResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RawToken == "" {
		t.Error("raw_token missing")
	}
	if resp.BackendURL == "" {
		t.Error("backend_url missing")
	}
	if resp.Nickname != "newrouter" {
		t.Errorf("nickname = %q", resp.Nickname)
	}
	if resp.JobID == "" {
		t.Error("job_id missing (register should still create an instant-success job for a uniform poll path)")
	}
	if len(resp.Steps) != 0 {
		t.Errorf("steps = %v, want empty (KindRegister's template is empty)", resp.Steps)
	}

	if got := relay.callCount(); got != 0 {
		t.Errorf("relay called %d times, want 0 — register must never touch the engine", got)
	}

	u, err := database.Users().GetByNickname("newrouter")
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(u.AWGMURL) != "https://awg.example" {
		t.Errorf("awgm_url not persisted: %q", stringValue(u.AWGMURL))
	}
	if stringValue(u.AWGMAuth) != "apikey" {
		t.Errorf("awgm_auth not persisted: %q", stringValue(u.AWGMAuth))
	}
	if stringValue(u.DeployMode) != "awgm" {
		t.Errorf("deploy_mode = %q, want awgm", stringValue(u.DeployMode))
	}
	if u.TelegramChatID == nil || *u.TelegramChatID != 100 {
		t.Errorf("telegram_chat_id = %v, want 100", u.TelegramChatID)
	}
}

func TestDashboardProvisionRegister_InvalidNickname400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"kind":"register","nickname":"NOT-VALID!","agent_kind":"static"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// --- provision (install now) ------------------------------------------------

func TestDashboardProvisionInstall_BuildsBootstrapJobWithVerifiedChecksumsAndPositiveBudgets(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	_, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	stubLatestVersion(t, "v0.13.9")
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "deadbeef"})

	body := `{"kind":"provision","nickname":"newrouter","agent_kind":"static","telegram_group":100,"thread_id":5,` +
		`"awgm_url":"https://awg.example","root_password":"rootpw","awgm_api_key":"key-123"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.JobID == "" {
		t.Fatal("job_id missing")
	}
	if len(resp.Steps) == 0 {
		t.Error("steps missing from the start response")
	}

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job state = %q, want success (hint=%q)", job.State, job.Hint)
	}

	if got := relay.callCount(); got != 1 {
		t.Fatalf("relay called %d times, want 1", got)
	}
	var captured awgmInstallJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Mode != "bootstrap_install" {
		t.Errorf("mode = %q, want bootstrap_install", captured.Mode)
	}
	if captured.TerminalUser != "root" || captured.TerminalPassword != "rootpw" {
		t.Errorf("terminal creds = %q/%q", captured.TerminalUser, captured.TerminalPassword)
	}
	if captured.APIKey != "key-123" {
		t.Errorf("api key = %q", captured.APIKey)
	}
	if captured.TargetVersion != "v0.13.9" {
		t.Errorf("target version = %q", captured.TargetVersion)
	}
	if captured.Checksums["wg-monitor-agent-linux-arm64"] != "deadbeef" {
		t.Errorf("checksums not passed through: %v", captured.Checksums)
	}
	if captured.InitScript == "" {
		t.Error("init script missing")
	}
	if captured.RawToken == "" {
		t.Error("raw token missing — the relay needs it to write config.yaml")
	}
	if captured.BackendURL != "https://wgmon.example" {
		t.Errorf("backend url = %q", captured.BackendURL)
	}
	if captured.ReleaseBase != "https://wgmon.example/v1/releases/download" {
		t.Errorf("release base = %q", captured.ReleaseBase)
	}

	until, ok := relay.deadlineUntil()
	if !ok {
		t.Fatal("relay ctx had no deadline at all — InstallBudget must be positive/bounded")
	}
	if until < time.Minute {
		t.Errorf("relay ctx deadline only %s away, want a healthy chunk of the 10m install budget", until)
	}
}

func TestDashboardProvisionInstall_MissingRootPassword400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"kind":"provision","nickname":"newrouter","agent_kind":"static","awgm_url":"https://awg.example"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if relay.callCount() != 0 {
		t.Error("relay must not be called when validation fails")
	}
}

func TestDashboardProvisionInstall_MissingAWGMURL400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"kind":"provision","nickname":"newrouter","agent_kind":"static","root_password":"rootpw"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardProvision_InvalidKind400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"kind":"nonsense","nickname":"x"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardProvisionInstall_SecondConcurrentCallRejected409(t *testing.T) {
	unblock := make(chan struct{})
	relay := &fakeProvisionRelay{rc: 0, block: unblock}
	_, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	stubVerifiedChecksums(t, map[string]string{"a": "b"})

	body := `{"kind":"provision","nickname":"busyrouter","agent_kind":"static",` +
		`"awgm_url":"https://awg.example","root_password":"rootpw","version":"v0.13.9"}`
	rec1 := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, body=%s", rec1.Code, rec1.Body.String())
	}
	var resp1 dashboardJobStartResp
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)

	// provision.Deps.Start acquires the single-flight lock synchronously
	// before returning (see runner.go's own doc), and this handler calls
	// Start synchronously within the request — so by the time postProvisionJSON
	// above has returned, "busyrouter" is already locked. No extra
	// synchronization is needed before firing the second request.
	rec2 := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second status = %d, want 409, body=%s", rec2.Code, rec2.Body.String())
	}

	close(unblock)
	waitForProvisionTerminal(t, store, resp1.JobID, time.Second)
}

// --- poll --------------------------------------------------------------

func TestDashboardProvisionPoll_UnknownID404(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/dashboard/provision/doesnotexist", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardProvisionPoll_ReturnsStartedJob(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	_, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	stubLatestVersion(t, "v0.13.9")
	stubVerifiedChecksums(t, map[string]string{"a": "b"})

	body := `{"kind":"provision","nickname":"pollme","agent_kind":"static","awgm_url":"https://awg.example","root_password":"rootpw"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/provision", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &started)

	waitForProvisionTerminal(t, store, started.JobID, time.Second)

	pollRec := httptest.NewRecorder()
	pollReq := httptest.NewRequest(http.MethodGet, "/v1/dashboard/provision/"+started.JobID, nil)
	mux.ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body=%s", pollRec.Code, pollRec.Body.String())
	}
	var job provision.Job
	if err := json.Unmarshal(pollRec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID != started.JobID {
		t.Errorf("job id = %q, want %q", job.ID, started.JobID)
	}
	if job.State != provision.StateSuccess {
		t.Errorf("state = %q, want success", job.State)
	}
}

// --- repair: repoint -----------------------------------------------------

func TestDashboardRepairRepoint_BuildsRepointJob(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	database, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("client-g", db.DeployInfo{AWGMURL: "https://awg.example"}); err != nil {
		t.Fatal(err)
	}

	body := `{"mode":"repoint","root_password":"rootpw","new_backend_url":"https://new.example"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.JobID == "" {
		t.Fatal("job_id missing")
	}

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job state = %q, want success (hint=%q)", job.State, job.Hint)
	}

	if got := relay.callCount(); got != 1 {
		t.Fatalf("relay calls = %d, want 1", got)
	}
	var captured awgmReviveJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Mode != "" {
		t.Errorf("mode = %q, want empty (default relay mode)", captured.Mode)
	}
	if captured.TerminalUser != "root" || captured.TerminalPassword != "rootpw" {
		t.Errorf("terminal creds = %q/%q", captured.TerminalUser, captured.TerminalPassword)
	}
	if !strings.Contains(captured.BootstrapScript, "https://new.example") {
		t.Errorf("bootstrap script missing new backend url: %s", captured.BootstrapScript)
	}
	// No token in a repoint job — the router keeps the enrollment it already
	// has on disk (see runner.go's RawToken doc: empty is correct here).
	if strings.Contains(captured.BootstrapScript, "raw_token") {
		t.Error("repoint script unexpectedly mentions a raw token")
	}
}

func TestDashboardRepairRepoint_NoStoredAWGMURL400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	body := `{"mode":"repoint","root_password":"rootpw","new_backend_url":"https://new.example"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// --- repair: reinstall ---------------------------------------------------

func TestDashboardRepairReinstall_BuildsBootstrapJobForExistingNick(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	database, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("client-g", db.DeployInfo{
		AWGMURL: "https://awg.example", LastDeployedVersion: "v0.13.5",
	}); err != nil {
		t.Fatal(err)
	}
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"})
	beforeHash := tokenHashOf(t, database, "client-g")

	body := `{"mode":"reinstall","root_password":"rootpw","version":"v0.13.9"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	job := waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job state = %q, want success (hint=%q)", job.State, job.Hint)
	}

	var captured awgmInstallJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Mode != "bootstrap_install" {
		t.Errorf("mode = %q, want bootstrap_install", captured.Mode)
	}
	if captured.TargetVersion != "v0.13.9" {
		t.Errorf("version = %q", captured.TargetVersion)
	}
	if captured.Checksums["wg-monitor-agent-linux-arm64"] != "cafebabe" {
		t.Errorf("checksums missing: %v", captured.Checksums)
	}
	if h := tokenHashOf(t, database, "client-g"); h == beforeHash {
		t.Error("expected a freshly re-minted token (hash should change)")
	}
}

func TestDashboardRepairReinstall_AntiDowngradeRejects400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("client-g", db.DeployInfo{
		AWGMURL: "https://awg.example", LastDeployedVersion: "v0.13.9",
	}); err != nil {
		t.Fatal(err)
	}

	body := `{"mode":"reinstall","root_password":"rootpw","version":"v0.13.5"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if relay.callCount() != 0 {
		t.Error("relay must not be called when anti-downgrade rejects")
	}
}

func TestDashboardRepairReinstall_AllowDowngradeOverrides(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0}
	database, store, mux := newProvisionTestHandler(t, relay, freshLastSeen)
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo("client-g", db.DeployInfo{
		AWGMURL: "https://awg.example", LastDeployedVersion: "v0.13.9",
	}); err != nil {
		t.Fatal(err)
	}
	stubVerifiedChecksums(t, map[string]string{"a": "b"})

	body := `{"mode":"reinstall","root_password":"rootpw","version":"v0.13.5","allow_downgrade":true}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body=%s", rec.Code, rec.Body.String())
	}
	var resp dashboardJobStartResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	waitForProvisionTerminal(t, store, resp.JobID, time.Second)
	if relay.callCount() != 1 {
		t.Error("relay should be called once allow_downgrade overrides the rejection")
	}
}

func TestDashboardRepair_UnknownNickname404(t *testing.T) {
	relay := &fakeProvisionRelay{}
	_, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	body := `{"mode":"repoint","root_password":"rootpw"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/ghost/repair", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardRepair_InvalidMode400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	body := `{"mode":"nonsense","root_password":"rootpw"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardRepair_MissingRootPassword400(t *testing.T) {
	relay := &fakeProvisionRelay{}
	database, _, mux := newProvisionTestHandler(t, relay, func(string) (time.Time, bool) { return time.Time{}, false })
	if _, err := database.Users().UpsertEnrollment("client-g", "tok-client-g-000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	body := `{"mode":"repoint"}`
	rec := postProvisionJSON(t, mux, http.MethodPost, "/v1/dashboard/agents/client-g/repair", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// --- isVersionDowngrade (unit) --------------------------------------------

func TestIsVersionDowngrade(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		current string
		want    bool
	}{
		{"empty current never a downgrade", "v0.13.5", "", false},
		{"empty target never a downgrade", "", "v0.13.5", false},
		{"older target is a downgrade", "v0.13.5", "v0.13.9", true},
		{"newer target is not a downgrade", "v0.13.9", "v0.13.5", false},
		{"equal is not a downgrade", "v0.13.9", "v0.13.9", false},
		// Regression pin: golang.org/x/mod/semver (upstream.SoftwareNewerThan)
		// treats a whole "-rcN" suffix as one opaque ASCII-lexical
		// identifier, so "rc9" sorts AFTER "rc10" ('9' > '1' as bytes) — the
		// wrong order for a fleet that has actually shipped double/
		// triple-digit RC tags (rc133 in production).
		// compareDashboardReleaseTags parses the RC suffix as an integer, so
		// rc9 < rc10 correctly — these two cases fail if isVersionDowngrade
		// is ever switched to the semver-based helper.
		{"rc9 -> rc10 is an upgrade, not a downgrade", "v0.13.0-rc10", "v0.13.0-rc9", false},
		{"rc10 -> rc9 IS a downgrade", "v0.13.0-rc9", "v0.13.0-rc10", true},
		{"full release outranks its own rc", "v0.13.5", "v0.13.5-rc50", false},
		{"an rc of an already-released version is a downgrade", "v0.13.5-rc50", "v0.13.5", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isVersionDowngrade(c.target, c.current); got != c.want {
				t.Errorf("isVersionDowngrade(%q,%q) = %v, want %v", c.target, c.current, got, c.want)
			}
		})
	}
}

func TestProvisionBudgetsArePositive(t *testing.T) {
	if provisionInstallBudget <= 0 {
		t.Fatalf("provisionInstallBudget = %v, must be a fixed positive constant", provisionInstallBudget)
	}
	if provisionVerifyBudget <= 0 {
		t.Fatalf("provisionVerifyBudget = %v, must be a fixed positive constant", provisionVerifyBudget)
	}
}
