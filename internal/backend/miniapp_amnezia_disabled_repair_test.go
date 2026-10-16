package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// v0.61.3: кабинет Amnezia Premium отключён -- автопочинка и её источник.

// Автопочинка: Amnezia не предлагается источником, включить с ним нельзя;
// выключить уже записанную настройку -- можно.
func TestAmneziaDisabled_AutorepairSource(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "amnezia_nl")
	const path = "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair"
	rec := env.do(t, cabOwner, http.MethodGet, path, "")
	resp := decodeAutorepair(t, rec.Body.Bytes())
	for _, s := range resp.Sources {
		if s.Provider == RepairProviderAmnezia {
			t.Fatalf("Amnezia среди источников: %+v", s)
		}
	}
	if resp.Suggested != nil && resp.Suggested.Provider == RepairProviderAmnezia {
		t.Fatalf("подсказан Amnezia: %+v", resp.Suggested)
	}

	rec = env.do(t, cabOwner, http.MethodPut, path, `{"enabled":true,"provider":"amnezia","option":"nl"}`)
	code, msg, _ := cabinetErrorBody(t, rec)
	expectAmneziaDisabled(t, "включение", code, msg, rec.Code)
	if _, found, _ := env.d.TunnelRepairSettings().Get(env.ownedID, "awg12"); found {
		t.Fatal("настройка с Amnezia записана")
	}

	if err := env.d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: env.ownedID, TunnelID: "awg12", Enabled: true, Provider: RepairProviderAmnezia, Option: "nl"}); err != nil {
		t.Fatal(err)
	}
	rec = env.do(t, cabOwner, http.MethodPut, path, `{"enabled":false,"provider":"amnezia","option":"nl"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("выключение: %d %s", rec.Code, rec.Body.String())
	}
}

// Источник починки: у Amnezia -- «нужен человек» с причиной, без обращения к
// кабинету ни за выпуском, ни за списком стран, ни за местом в подписке.
func TestAmneziaDisabled_RepairSourceNeverAsksCabinet(t *testing.T) {
	cab := &countingCabinet{fakeCabinet: fakeCabinet{conf: []byte("[Interface]\n"), accounts: map[string]VPNAccount{
		"amnezia": {Provider: "amnezia", Connected: true, Options: []VPNOption{{ID: "nl", Issued: true}, {ID: "de"}}},
	}}}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	ctx := context.Background()
	check := func(what string, err error) {
		t.Helper()
		var nh *linkrepair.NeedHuman
		if !errors.As(err, &nh) || nh.Action != linkrepair.ActAmneziaDisabled || !errors.Is(err, ErrAmneziaDisabled) {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := src.Issue(ctx, id, RepairProviderAmnezia, "nl")
	check("Issue", err)
	_, err = src.Fresh(ctx, id, RepairProviderAmnezia, "nl")
	check("Fresh", err)
	_, err = src.Options(ctx, id, RepairProviderAmnezia)
	check("Options", err)
	_, err = src.HasRoom(ctx, id, RepairProviderAmnezia)
	check("HasRoom", err)
	if n := cab.count(); n != 0 {
		t.Fatalf("кабинет спрошен %d раз", n)
	}
}

// countingCabinet -- fakeCabinet, считающий любые вызовы.
type countingCabinet struct {
	fakeCabinet
	mu    sync.Mutex
	calls int
}

func (c *countingCabinet) Account(ctx context.Context, routerID int64, provider string) (VPNAccount, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.fakeCabinet.Account(ctx, routerID, provider)
}

func (c *countingCabinet) IssueConfig(ctx context.Context, routerID int64, provider, optionID string) (VPNIssuedConfig, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.fakeCabinet.IssueConfig(ctx, routerID, provider, optionID)
}

func (c *countingCabinet) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// Уже сохранённая настройка автопочинки с Amnezia (смена страны разрешена):
// лесенка кончается «нужен человек» с причиной, кабинет не спрошен, отметка о
// смене страны не записана.
func TestAmneziaDisabled_SavedAutorepairEndsNeedHumanWithoutSpend(t *testing.T) {
	// С движком, знающим выключенный источник (как в бою), и без него --
	// тогда отказывает сам источник: выпуска нет в обоих случаях.
	for _, wired := range []bool{true, false} {
		t.Run(fmt.Sprintf("SourceOff=%v", wired), func(t *testing.T) { savedAmneziaRepair(t, wired) })
	}
}

func savedAmneziaRepair(t *testing.T, wired bool) {
	d, routerID, owner := linkRepairDeps(t)
	if err := d.DB.Users().UpdateDeployInfo("router-owned", db.DeployInfo{LastDeployedVersion: "v0.60.0"}); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: routerID, TunnelID: "awg11", Enabled: true,
		Provider: RepairProviderAmnezia, Option: "nl", AllowRelocate: true, UpdatedBy: owner}); err != nil {
		t.Fatal(err)
	}
	cab := &countingCabinet{fakeCabinet: fakeCabinet{conf: []byte("[Interface]\n"), accounts: map[string]VPNAccount{
		"amnezia": {Provider: "amnezia", Connected: true, Options: []VPNOption{{ID: "nl", Issued: true}, {ID: "de"}}},
	}}}
	var spentMu sync.Mutex
	var spent []string
	agent := &repairAgent{}
	d.LinkRepair.Source = RepairSource(cab, nil, d.DB, 999)
	d.LinkRepair.Commands = agent
	d.LinkRepair.Probe.Commands = agent
	if wired {
		d.LinkRepair.SourceOff = RepairSourceOff
	}
	d.LinkRepair.SpendRelocation = func(routerID int64, tunnelID, option string) error {
		spentMu.Lock()
		spent = append(spent, option)
		spentMu.Unlock()
		return LinkRepairSpendRelocation(d.DB)(routerID, tunnelID, option)
	}

	rr := doRepair(t, NewMux(d), http.MethodPost, fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), owner, `{"check_name":"tunnel_awg11"}`)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("код %d: %s", rr.Code, rr.Body.String())
	}
	var resp miniappReplaceResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	job := waitRepairJob(t, d, resp.JobID)
	if job.State != provision.StateFailed || job.Hint != linkrepair.ActAmneziaDisabled {
		t.Fatalf("ждали «нужен человек» с %q, получили state=%s hint=%q", linkrepair.ActAmneziaDisabled, job.State, job.Hint)
	}
	if acts := agent.actions(); !acts["tunnel_restart"] || acts["tunnel_import"] {
		t.Fatalf("команды роутеру: %v", acts)
	}
	if n := cab.count(); n != 0 {
		t.Fatalf("кабинет спрошен %d раз", n)
	}
	spentMu.Lock()
	defer spentMu.Unlock()
	if len(spent) != 0 {
		t.Fatalf("отметка о смене страны: %v", spent)
	}
	s, _, err := d.DB.TunnelRepairSettings().Get(routerID, "awg11")
	if err != nil || strings.TrimSpace(s.RelocateSpent) != "" {
		t.Fatalf("RelocateSpent=%q err=%v", s.RelocateSpent, err)
	}
}

// actions -- какие команды получил агент.
func (a *repairAgent) actions() map[string]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]bool{}
	for _, act := range a.byID {
		out[act] = true
	}
	return out
}

// Сохранённая настройка с Amnezia на экране -- не «включена», а «только
// перезапуск» с причиной; в списке меток -- своё состояние source_off.
func TestAmneziaDisabled_SavedSettingShownNotWorking(t *testing.T) {
	env := autorepairEnv(t)
	seedAutorepairTunnel(t, env, "awg12", "amnezia_nl")
	if err := env.d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: env.ownedID, TunnelID: "awg12", TunnelName: "amnezia_nl",
		Enabled: true, Provider: RepairProviderAmnezia, Option: "nl"}); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/tunnels/awg12/autorepair", "")
	if resp := decodeAutorepair(t, rec.Body.Bytes()); !resp.Enabled || resp.SourceOff != linkrepair.ActAmneziaDisabled {
		t.Fatalf("экран: %+v", resp)
	}
	var list struct {
		Tunnels map[string]string `json:"tunnels"`
	}
	rec = env.do(t, cabOwner, http.MethodGet, "/v1/miniapp/routers/{id}/autorepair", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Tunnels["awg12"] != autorepairStateSourceOff {
		t.Fatalf("список: %+v", list)
	}
	if RepairSourceOff(RepairProviderHideMy) != "" || RepairSourceOff(RepairProviderAwg3) != "" || RepairSourceOff("") != "" {
		t.Fatal("выключен только «Amnezia Premium»")
	}
}
