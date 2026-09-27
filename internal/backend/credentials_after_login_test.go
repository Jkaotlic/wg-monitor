package backend

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

func saveWorkingCreds(t *testing.T, d *db.DB, routerID int64) revive.Secrets {
	t.Helper()
	working := revive.NewSecrets("рабочий-пароль", "", "", "")
	box, _ := revive.NewBox(credTestKey)
	nonce, ct, err := box.Seal(routerID, working)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.RouterCredentials().Put(routerID, nonce, ct, time.Now()); err != nil {
		t.Fatal(err)
	}
	return working
}

// REV-03: непроверенный пароль переустановки не затирает рабочий
// сохранённый. Сохраняется только после входа в терминал (config_written),
// как у провижининга.
func TestMiniappReinstallUnverifiedPasswordKeepsWorkingOne(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	env.relay.lines = nil // вход в терминал не прошёл: маркеров нет
	env.relay.rc = 1
	env.relay.err = errors.New("auth_failed")
	if err := env.d.Users().UpdateDeployInfo("router-owned", db.DeployInfo{AWGMURL: "https://panel.example.com", LastDeployedVersion: "v0.35.0"}); err != nil {
		t.Fatal(err)
	}
	if err := env.d.Users().UpdateLastSeen(env.ownedID); err != nil {
		t.Fatal(err)
	}
	working := saveWorkingCreds(t, env.d, env.ownedID)
	rec := miniappDo(t, env.h, http.MethodPost, reinstallPath(env.ownedID), secretsBody(map[string]any{"confirm": "router-owned"}), 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("переустановка: код %d (%s)", rec.Code, rec.Body.String())
	}
	waitForProvisionTerminal(t, env.store, decodeJobStart(t, "переустановка", rec.Body.Bytes()), time.Second)
	got, ok := storedCreds(t, env.d, env.ownedID)
	if !ok || !got.Equal(working) {
		t.Fatal("непроверенный пароль переустановки затёр рабочий сохранённый")
	}
}

// Постановка оживления пароль не проверяет -- и сохранять его рано.
func TestMiniappReviveScheduleKeepsWorkingPassword(t *testing.T) {
	env := newAdminOpsEnv(t, withRealRevive(t))
	working := saveWorkingCreds(t, env.d, env.ownedID)
	body := reviveBody("router-owned", map[string]any{"awgm_url": "https://router.example.com"})
	rec := miniappDo(t, env.h, http.MethodPost, revivePath(env.ownedID), body, 999)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("оживление: код %d (%s)", rec.Code, rec.Body.String())
	}
	got, ok := storedCreds(t, env.d, env.ownedID)
	if !ok || !got.Equal(working) {
		t.Fatal("постановка оживления затёрла рабочий сохранённый пароль непроверенным")
	}
}

// Движок говорит оживлению, прошёл ли вход: config_written начался --
// прошёл, даже если установка потом упала; не начался -- нет.
func TestReviveEngine_OutcomeReportsVerifiedLogin(t *testing.T) {
	store := provision.NewStore()
	e := NewReviveEngine(ReviveEngineDeps{Provision: provision.Deps{Store: store}})
	for _, tc := range []struct {
		status provision.StepStatus
		want   bool
	}{{provision.StepPending, false}, {provision.StepDone, true}, {provision.StepFailed, true}} {
		job := store.Create(provision.KindRepairReinstall, "bronya", provision.Template(provision.KindRepairReinstall))
		store.Update(job.ID, func(j *provision.Job) {
			j.State = provision.StateFailed
			for i := range j.Steps {
				if j.Steps[i].Name == provision.StepConfigWritten {
					j.Steps[i].Status = tc.status
				}
			}
		})
		out, ok := e.Outcome(job.ID)
		if !ok || !out.Finished || out.CredentialsVerified != tc.want {
			t.Fatalf("config_written=%s: %+v", tc.status, out)
		}
	}
}
