package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func linkRepairDeps(t *testing.T) (Deps, int64, int64) {
	t.Helper()
	d, ownedID, _, telegramUserID := seedMiniappFleet(t)
	seedMiniappTunnelEvent(t, d, ownedID, "awg11", "")
	sink := &replaceSink{}
	store := provision.NewStore()
	eng := &replace.Deps{
		Store:          store,
		Commands:       sink,
		Cabinet:        stubCabinet{},
		BaseCtx:        context.Background(),
		AwaitStep:      50 * time.Millisecond,
		HandshakeTries: 1,
		HandshakeWait:  time.Millisecond,
		Sleep:          func(context.Context, time.Duration) {},
	}
	repair := &linkrepair.Deps{
		Store:      store,
		Probe:      *eng,
		Source:     RepairSource(nil, nil, d, 999),
		Settings:   LinkRepairSettings(d, nil),
		SaveOption: LinkRepairSaveOption(d, nil),
		Attempts:   linkrepair.Attempts{KV: d.KV()},
		Commands:   sink,
		BaseCtx:    context.Background(),
		AwaitStep:  50 * time.Millisecond,
	}
	return Deps{
		DB:                  d,
		TelegramBotToken:    "test-bot-token",
		TelegramAdminUserID: 999,
		CommandSink:         sink,
		Replace:             eng,
		LinkRepair:          repair,
	}, ownedID, telegramUserID
}

func doRepair(t *testing.T, h http.Handler, method, path string, tgUser int64, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(miniappSessionCookieFor(t, "test-bot-token", tgUser))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Пока идёт замена конфига, починку начинать нельзя -- замок общий.
func TestMiniappRepair_ConflictWithReplace(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	u, err := d.DB.Users().GetByID(routerID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !d.LinkRepair.Store.TryLock(u.Nickname) {
		t.Fatal("замок должен браться")
	}

	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser,
		`{"check_name":"tunnel_awg11"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("код %d, хотим 409: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "уже идёт") {
		t.Fatalf("отказ обязан объясняться словами: %s", rr.Body.String())
	}
}

// Нечинибельная поломка -- 422 со словами, а не 500 и не молчание.
func TestMiniappRepair_NoScenario(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser,
		`{"check_name":"external_reach"}`)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("код %d, хотим 422: %s", rr.Code, rr.Body.String())
	}
}

// Чужой роутер не виден даже для отказа: 404, а не 409 и не 422.
func TestMiniappRepair_StrangerGets404(t *testing.T) {
	d, routerID, _ := linkRepairDeps(t)
	rr := doRepair(t, NewMux(d), http.MethodPost,
		fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), 4242,
		`{"check_name":"tunnel_awg11"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("код %d, хотим 404", rr.Code)
	}
}

// Ход починки отдаёт экрану подсказку задания: на провале это «что делать».
func TestMiniappRepair_StatusCarriesHint(t *testing.T) {
	d, routerID, tgUser := linkRepairDeps(t)
	u, err := d.DB.Users().GetByID(routerID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	job := d.LinkRepair.Store.Create(linkrepair.KindLinkRepair, u.Nickname, linkrepair.Steps())
	d.LinkRepair.Store.Update(job.ID, func(j *provision.Job) {
		j.Target = "tunnel_awg11"
		j.State = provision.StateFailed
		j.Hint = linkrepair.ActAmneziaKey
	})
	rr := doRepair(t, NewMux(d), http.MethodGet, fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tgUser, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("код %d: %s", rr.Code, rr.Body.String())
	}
	var resp miniappReplaceResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("ответ: %v", err)
	}
	if resp.Hint != linkrepair.ActAmneziaKey || resp.State != "failed" {
		t.Fatalf("ответ без «что делать»: %+v", resp)
	}
}

// repairAgent -- поддельный агент для лесенки целиком: снимок, где awg11
// лежит, а резерв awg10 жив; остальные команды отвечают «ок», и перезапуск
// не помогает -- лесенка доходит до выпуска конфига.
type repairAgent struct {
	mu   sync.Mutex
	byID map[string]string
}

func (a *repairAgent) Enqueue(_ int64, cmd wire.Command) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byID == nil {
		a.byID = map[string]string{}
	}
	a.byID[cmd.ID] = cmd.Action
	return nil
}

func (a *repairAgent) AwaitResult(_ context.Context, _ int64, id string, _ time.Duration) (*wire.CommandResult, bool) {
	a.mu.Lock()
	action := a.byID[id]
	a.mu.Unlock()
	out := ""
	if action == "route_status" {
		b, _ := json.Marshal(wire.RouteSnapshot{
			Tunnels: []wire.TunnelMeta{{ID: "awg11", Name: "Дача"}, {ID: "awg10", Name: "Работа", HasHandshake: true, HandshakeAge: 30}},
			Policies: []wire.RoutePolicySummary{{Name: "HydraRoute", Interfaces: []wire.RoutePolicyInterface{
				{Bind: "OpkgTun11", Name: "Дача", TunnelID: "awg11", Role: "active", Order: 1},
				{Bind: "OpkgTun10", Name: "Работа", TunnelID: "awg10", Role: "fallback", Available: true, Order: 2},
			}}},
		})
		out = string(b)
	}
	return &wire.CommandResult{ID: id, Status: "ok", Output: out}, true
}

// BE-02: ручной «Починить» выпускает конфиг от имени нажавшего. Оператор
// роутера без допуска к панели своего сервера пира на ней не заводит, даже
// если автопочинку включал владелец с допуском; владелец -- заводит.
func TestMiniappRepair_ManualIssueNeedsPanelRight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		presser int64
		issued  bool
	}{
		{"оператор без допуска", 300, false},
		{"владелец с допуском", 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, routerID, owner := linkRepairDeps(t)
			if err := d.DB.Users().UpdateDeployInfo("router-owned", db.DeployInfo{LastDeployedVersion: "v0.60.0"}); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.RouterOperators().Add(routerID, 300, 999); err != nil {
				t.Fatal(err)
			}
			if err := d.DB.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: routerID, TunnelID: "awg11", Enabled: true,
				Provider: RepairProviderAwg3, Option: "vps1/wg0", UpdatedBy: owner}); err != nil {
				t.Fatal(err)
			}
			panels := &fakeAwg3{issuers: map[string][]int64{"vps1": {owner}},
				routerConf: awg3panel.RouterConfig{Conf: []byte("[Interface]\n"), PeerID: "p1"}}
			agent := &repairAgent{}
			d.LinkRepair.Source = RepairSource(nil, panels, d.DB, 999)
			d.LinkRepair.Commands = agent
			d.LinkRepair.Probe.Commands = agent
			d.LinkRepair.Probe.HandshakeTries = 1

			rr := doRepair(t, NewMux(d), http.MethodPost,
				fmt.Sprintf("/v1/miniapp/routers/%d/repair", routerID), tc.presser,
				`{"check_name":"tunnel_awg11"}`)
			if rr.Code != http.StatusAccepted {
				t.Fatalf("код %d: %s", rr.Code, rr.Body.String())
			}
			var resp miniappReplaceResp
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			job := waitRepairJob(t, d, resp.JobID)
			panels.mu.Lock()
			calls := append([]string(nil), panels.routerCalls...)
			panels.mu.Unlock()
			if tc.issued {
				if len(calls) == 0 {
					t.Fatalf("у владельца с допуском выпуск не дошёл до панели: job=%+v", job)
				}
				return
			}
			if len(calls) != 0 {
				t.Fatalf("без допуска панель спрошена: %v", calls)
			}
			if job.State != provision.StateFailed || job.Hint != linkrepair.ActNoPanelAccess {
				t.Fatalf("ждали провал с %q, получили state=%s hint=%q", linkrepair.ActNoPanelAccess, job.State, job.Hint)
			}
		})
	}
}

func waitRepairJob(t *testing.T, d Deps, id string) provision.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := d.LinkRepair.Store.Get(id); ok && j.State != provision.StateRunning {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("починка %s не закончилась", id)
	return provision.Job{}
}
