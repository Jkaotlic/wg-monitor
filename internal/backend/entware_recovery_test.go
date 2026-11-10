package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"strings"
	"testing"
	"time"
)

func TestEntwareRecoveryRequiresVerifiedRelayResultAndWorksForOldAgent(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit0-is-insufficient", true: "verified"}[confirmed], func(t *testing.T) {
			d, database := newReinstallCoreDeps(t, &fakeProvisionRelay{})
			u := seedReinstallRouter(t, database, "client-a", "https://router.example.com", "v0.66.3-rc2")
			svc := NewEntwareRecovery(d)
			svc.Load = func(int64) (revive.Secrets, bool, error) {
				return revive.NewSecrets("", "admin", "fixture-panel", ""), true, nil
			}
			svc.Relay = func(_ context.Context, _ string, body []byte, line func(string)) (int, error) {
				var job map[string]any
				if err := json.Unmarshal(body, &job); err != nil {
					t.Fatal(err)
				}
				if job["terminal_password"] != nil || job["mode"] != "entware_recover" {
					t.Error("root/terminal path used")
				}
				nonce := job["recovery_nonce"].(string)
				line("secret arbitrary untrusted panel output")
				if confirmed {
					for _, step := range []string{"recovery_check", "recovery_hold", "recovery_links", "recovery_verify"} {
						line("__WG_RECOVERY__ " + nonce + " " + step)
					}
					line("__WG_RECOVERY_RESULT__ " + nonce + ` {"held":true,"shell_ok":true,"cleanup_ok":true,"links_created":12,"warnings":[]}`)
				}
				return 0, nil
			}
			id, err := svc.Start(u.ID)
			if err != nil {
				t.Fatal(err)
			}
			job := waitForProvisionTerminal(t, d.Provision.Store, id, time.Second)
			if (job.State == provision.StateSuccess) != confirmed {
				t.Fatalf("wrong result: %+v", job)
			}
			raw, _ := json.Marshal(job)
			if strings.Contains(string(raw), "secret arbitrary") || strings.Contains(string(raw), "fixture-panel") {
				t.Fatal("secret/raw output persisted")
			}
		})
	}
}

func TestEntwareRecoveryHTTPGates(t *testing.T) {
	env := newAdminOpsEnv(t)
	path := fmt.Sprintf("/v1/miniapp/routers/%d/entware/recover", env.ownedID)
	for _, uid := range []int64{100, 555} {
		rec := miniappDo(t, env.h, "POST", path, `{"confirm":true}`, uid)
		if rec.Code != 404 {
			t.Fatalf("nonadmin %d", rec.Code)
		}
	}
	rec := miniappDo(t, env.h, "POST", path, `{}`, 999)
	if rec.Code != 400 {
		t.Fatalf("missing confirmation %d %s", rec.Code, rec.Body.String())
	}
	rec = miniappDo(t, env.h, "GET", "/v1/miniapp/entware/recovery-script", "", 999)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "#!/opt/bin/busybox sh") {
		t.Fatalf("download %d %s", rec.Code, rec.Body.String())
	}
	rec = miniappDo(t, env.h, "GET", "/v1/miniapp/entware/recovery-script", "", 100)
	if rec.Code != 404 {
		t.Fatal("script leaked to nonadmin")
	}
}

func TestEntwareRecoveryHTTPStartsOldAgentAndSharesProvisionLock(t *testing.T) {
	env := newAdminOpsEnv(t, func(d *Deps) {
		svc := NewEntwareRecovery(*d)
		svc.Load = func(int64) (revive.Secrets, bool, error) {
			return revive.NewSecrets("", "admin", "fixture-panel", ""), true, nil
		}
		svc.Relay = func(_ context.Context, _ string, body []byte, line func(string)) (int, error) {
			var cfg map[string]any
			if err := json.Unmarshal(body, &cfg); err != nil {
				return 1, err
			}
			nonce := cfg["recovery_nonce"].(string)
			for _, step := range recoverySteps {
				line("__WG_RECOVERY__ " + nonce + " " + step)
			}
			line("__WG_RECOVERY_RESULT__ " + nonce + ` {"held":true,"shell_ok":true,"cleanup_ok":true,"links_created":0,"warnings":[]}`)
			return 0, nil
		}
		d.EntwareRecovery = svc
	})
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://router.example.com", LastDeployedVersion: "v0.66.3-rc2"}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/v1/miniapp/routers/%d/entware/recover", env.ownedID)
	if !env.store.TryLock("router-owned") {
		t.Fatal("lock setup")
	}
	rec := miniappDo(t, env.h, "POST", path, `{"confirm":true}`, 999)
	if rec.Code != 409 {
		t.Fatalf("parallel repair accepted: %d", rec.Code)
	}
	env.store.Unlock("router-owned")
	rec = miniappDo(t, env.h, "POST", path, `{"confirm":true,"script":"evil"}`, 999)
	if rec.Code != 400 {
		t.Fatal("script input accepted")
	}
	rec = miniappDo(t, env.h, "POST", path, `{"confirm":true}`, 999)
	if rec.Code != 202 {
		t.Fatalf("old agent rejected: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeJobStart(t, "entware", rec.Body.Bytes())
	job := waitForProvisionTerminal(t, env.store, id, time.Second)
	if job.State != provision.StateSuccess {
		t.Fatalf("job not verified: %+v", job)
	}
}
