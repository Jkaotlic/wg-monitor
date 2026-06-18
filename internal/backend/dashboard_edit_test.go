package backend

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func seedEditAgent(t *testing.T, d *db.DB) {
	t.Helper()
	if _, err := d.Users().UpsertEnrollment("bronya", "tok-bronya-0000000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateDeployInfo("bronya", db.DeployInfo{
		Ring:                "rc",
		DeployMode:          "ssh",
		AWGMURL:             "https://old.router.example",
		AWGMAuth:            "web",
		ExpectedMAC:         "aa:bb:cc:dd:ee:ff",
		LastDeployedVersion: "v0.13.0",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardEditAgentChangesDomainAndPreservesBlankFields(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedEditAgent(t, d)
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})

	// Operator only changes the router domain; everything else left blank.
	req := httptest.NewRequest(http.MethodPut, "/v1/dashboard/agents/bronya",
		strings.NewReader(`{"awgm_url":"https://new.router.example"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	u, err := d.Users().GetByNickname("bronya")
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(u.AWGMURL) != "https://new.router.example" {
		t.Fatalf("awgm_url not updated: %q", stringValue(u.AWGMURL))
	}
	// Blank fields in the request must NOT wipe existing values.
	if stringValue(u.Ring) != "rc" || stringValue(u.DeployMode) != "ssh" ||
		stringValue(u.AWGMAuth) != "web" || stringValue(u.ExpectedMAC) != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("blank-field edit wiped data: ring=%q mode=%q auth=%q mac=%q",
			stringValue(u.Ring), stringValue(u.DeployMode), stringValue(u.AWGMAuth), stringValue(u.ExpectedMAC))
	}
	// System-managed field preserved.
	if stringValue(u.LastDeployedVersion) != "v0.13.0" {
		t.Fatalf("last_deployed_version wiped: %q", stringValue(u.LastDeployedVersion))
	}
}

func TestDashboardEditAgentRejectsBadAWGMURL(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedEditAgent(t, d)
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})

	req := httptest.NewRequest(http.MethodPut, "/v1/dashboard/agents/bronya",
		strings.NewReader(`{"awgm_url":"ftp://nope"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The bad request must not have changed the stored URL.
	u, _ := d.Users().GetByNickname("bronya")
	if stringValue(u.AWGMURL) != "https://old.router.example" {
		t.Fatalf("stored url changed on rejected edit: %q", stringValue(u.AWGMURL))
	}
}

func TestDashboardEditAgentUnknownNickname404(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})

	req := httptest.NewRequest(http.MethodPut, "/v1/dashboard/agents/ghost",
		strings.NewReader(`{"awgm_url":"https://x.example"}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDashboardEditAgentRequiresAuth(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	seedEditAgent(t, d)
	h := NewMux(Deps{DB: d, DashboardToken: "secret"})

	req := httptest.NewRequest(http.MethodPut, "/v1/dashboard/agents/bronya",
		strings.NewReader(`{"awgm_url":"https://new.router.example"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
