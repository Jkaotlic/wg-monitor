package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

func newReinstallCoreDeps(t *testing.T, relay *fakeProvisionRelay) (Deps, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return Deps{
		DB:            database,
		PublicBaseURL: "https://wgmon.example.com",
		PublicIP:      "203.0.113.9",
		Provision: provision.Deps{
			Store:    provision.NewStore(),
			BaseCtx:  context.Background(),
			Relay:    relay.run,
			LastSeen: freshLastSeen,
		},
	}, database
}

func seedReinstallRouter(t *testing.T, database *db.DB, nick, awgmURL, lastVersion string) *db.User {
	t.Helper()
	if _, err := database.Users().UpsertEnrollment(nick, "tok-"+nick+"-0000000000000000000000", db.KindStatic, 0); err != nil {
		t.Fatal(err)
	}
	if err := database.Users().UpdateDeployInfo(nick, db.DeployInfo{AWGMURL: awgmURL, LastDeployedVersion: lastVersion}); err != nil {
		t.Fatal(err)
	}
	u, err := database.Users().GetByNickname(nick)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestStartRepairReinstall_StartsJobWithoutHTTP(t *testing.T) {
	relay := &fakeProvisionRelay{rc: 0, lines: []string{"__WG_STEP__ config_written"}}
	d, database := newReinstallCoreDeps(t, relay)
	u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "v0.13.5")
	stubVerifiedChecksums(t, map[string]string{"wg-monitor-agent-linux-arm64": "cafebabe"})

	jobID, version, serr := startRepairReinstall(context.Background(), d, "bronya", u, reinstallInput{
		RootPassword: "rootpw", AWGMLogin: "admin", AWGMPassword: "panelpw", Version: "v0.13.9",
	})
	if serr != nil {
		t.Fatalf("serr = %+v", serr)
	}
	if jobID == "" || version != "v0.13.9" {
		t.Fatalf("jobID=%q version=%q", jobID, version)
	}
	job := waitForProvisionTerminal(t, d.Provision.Store, jobID, time.Second)
	if job.State != provision.StateSuccess || job.Kind != provision.KindRepairReinstall {
		t.Fatalf("job = %+v", job)
	}
	var captured awgmInstallJob
	if err := json.Unmarshal(relay.capturedJobJSON(), &captured); err != nil {
		t.Fatal(err)
	}
	if captured.TerminalPassword != "rootpw" || captured.Login != "admin" || captured.Password != "panelpw" {
		t.Fatalf("учётные данные не дошли до движка: %+v", captured)
	}
	if captured.BaseURL != "https://awg.example.com" || captured.Mode != "bootstrap_install" {
		t.Fatalf("job: %+v", captured)
	}
}

func TestStartRepairReinstall_TypedErrors(t *testing.T) {
	t.Run("no awgm url", func(t *testing.T) {
		relay := &fakeProvisionRelay{}
		d, database := newReinstallCoreDeps(t, relay)
		u := seedReinstallRouter(t, database, "bronya", "", "")
		_, _, serr := startRepairReinstall(context.Background(), d, "bronya", u, reinstallInput{RootPassword: "x", Version: "v0.13.9"})
		if serr == nil || serr.Code != "no_awgm_url" || serr.Status != http.StatusBadRequest {
			t.Fatalf("serr = %+v", serr)
		}
		if relay.callCount() != 0 {
			t.Fatal("движок не должен запускаться")
		}
	})
	t.Run("downgrade", func(t *testing.T) {
		relay := &fakeProvisionRelay{}
		d, database := newReinstallCoreDeps(t, relay)
		u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "v0.13.9")
		_, _, serr := startRepairReinstall(context.Background(), d, "bronya", u, reinstallInput{RootPassword: "x", Version: "v0.13.5"})
		if serr == nil || serr.Code != "downgrade_rejected" {
			t.Fatalf("serr = %+v", serr)
		}
	})
	t.Run("lock busy", func(t *testing.T) {
		relay := &fakeProvisionRelay{}
		d, database := newReinstallCoreDeps(t, relay)
		u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "")
		stubVerifiedChecksums(t, map[string]string{"a": "b"})
		if !d.Provision.Store.TryLock("bronya") {
			t.Fatal("lock")
		}
		_, _, serr := startRepairReinstall(context.Background(), d, "bronya", u, reinstallInput{RootPassword: "x", Version: "v0.13.9"})
		if serr == nil || serr.Code != "provision_already_running" || serr.Status != http.StatusConflict {
			t.Fatalf("serr = %+v", serr)
		}
	})
	t.Run("engine not wired", func(t *testing.T) {
		relay := &fakeProvisionRelay{}
		d, database := newReinstallCoreDeps(t, relay)
		u := seedReinstallRouter(t, database, "bronya", "https://awg.example.com", "")
		d.Provision.Store = nil
		_, _, serr := startRepairReinstall(context.Background(), d, "bronya", u, reinstallInput{RootPassword: "x", Version: "v0.13.9"})
		if serr == nil || serr.Code != "provision_not_configured" || serr.Status != http.StatusServiceUnavailable {
			t.Fatalf("serr = %+v", serr)
		}
	})
}
