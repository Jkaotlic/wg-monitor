package backend

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
)

// FreshConfigForRouter у фейковой панели -- здесь, рядом с тестами источника
// починки: вызов пишется в те же routerCalls с меткой «fresh|», чтобы тест
// видел, какой из двух путей выбран.
func (f *fakeAwg3) FreshConfigForRouter(_ context.Context, id, iface, nick string) (awg3panel.RouterConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routerCalls = append(f.routerCalls, "fresh|"+id+"|"+iface+"|"+nick)
	return f.routerConf, f.routerErr
}

func repairSourceEnv(t *testing.T, cab VPNCabinet, panels Awg3Panels) (linkrepair.Source, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("дача", "tok-repair-source", "", "")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return RepairSource(cab, panels, d), id
}

func TestRepairSource_AmneziaNoKeyNeedsHuman(t *testing.T) {
	cab := &fakeCabinet{err: errors.New("ключ Amnezia Premium не сохранён для этого роутера")}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	_, err := src.Issue(context.Background(), id, "amnezia", "nl")
	var nh *linkrepair.NeedHuman
	if !errors.As(err, &nh) {
		t.Fatalf("ждали NeedHuman, получили %v", err)
	}
	if nh.Action != linkrepair.ActAmneziaKey {
		t.Fatalf("действие %q, ждали %q", nh.Action, linkrepair.ActAmneziaKey)
	}
}

func TestRepairSource_HideMyRefusalNeedsHuman(t *testing.T) {
	cab := &fakeCabinet{err: errors.New("код HideMy.name не сохранён для этого роутера")}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	_, err := src.Fresh(context.Background(), id, "hidemyname", "srv-1")
	var nh *linkrepair.NeedHuman
	if !errors.As(err, &nh) || nh.Action != linkrepair.ActHideMyCode {
		t.Fatalf("ждали NeedHuman с ActHideMyCode, получили %v", err)
	}
}

func TestRepairSource_AmneziaIssuePassesConfig(t *testing.T) {
	cab := &fakeCabinet{conf: []byte("[Interface]\n")}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	got, err := src.Issue(context.Background(), id, "amnezia", "nl")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if got.TunnelName != "amnezia_nl" || string(got.Conf) != "[Interface]\n" {
		t.Fatalf("выпуск: %+v", got)
	}
	if len(cab.issued) != 1 || cab.issued[0] != "amnezia:nl" {
		t.Fatalf("кабинет спрошен не тем: %v", cab.issued)
	}
}

func TestRepairSource_Awg3IssueSplitsOption(t *testing.T) {
	panels := &fakeAwg3{routerConf: awg3panel.RouterConfig{Conf: []byte("[Interface]\n"), PeerID: "p1", Reused: true}}
	src, id := repairSourceEnv(t, &fakeCabinet{}, panels)
	got, err := src.Issue(context.Background(), id, "awg3", "vps1/wg0")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(panels.routerCalls) != 1 || panels.routerCalls[0] != "vps1|wg0|дача" {
		t.Fatalf("панель спрошена не так: %v", panels.routerCalls)
	}
	if got.TunnelName != awg3panel.TunnelName("vps1", "wg0") || got.Backend != "nativewg" || len(got.Conf) == 0 {
		t.Fatalf("выпуск: %+v", got)
	}
}

func TestRepairSource_Awg3FreshAlwaysNewPeer(t *testing.T) {
	panels := &fakeAwg3{routerConf: awg3panel.RouterConfig{Conf: []byte("[Interface]\n"), PeerID: "p2"}}
	src, id := repairSourceEnv(t, &fakeCabinet{}, panels)
	if _, err := src.Fresh(context.Background(), id, "awg3", "vps1/wg0"); err != nil {
		t.Fatalf("Fresh: %v", err)
	}
	if len(panels.routerCalls) != 1 || panels.routerCalls[0] != "fresh|vps1|wg0|дача" {
		t.Fatalf("ждали FreshConfigForRouter: %v", panels.routerCalls)
	}
}

func TestRepairSource_Awg3PanelErrorNeedsHuman(t *testing.T) {
	panels := &fakeAwg3{routerErr: errors.New("панель не ответила")}
	src, id := repairSourceEnv(t, &fakeCabinet{}, panels)
	for _, call := range []func() error{
		func() error { _, err := src.Issue(context.Background(), id, "awg3", "vps1/wg0"); return err },
		func() error { _, err := src.Fresh(context.Background(), id, "awg3", "vps1/wg0"); return err },
	} {
		var nh *linkrepair.NeedHuman
		if err := call(); !errors.As(err, &nh) || nh.Action != linkrepair.ActVPSPanel("vps1") {
			t.Fatalf("ждали NeedHuman с ActVPSPanel, получили %v", err)
		}
	}
}

// Вариант без «/» -- настройка испорчена: чинить нечем, нужен человек.
func TestRepairSource_Awg3BadOptionNeedsHuman(t *testing.T) {
	panels := &fakeAwg3{}
	src, id := repairSourceEnv(t, &fakeCabinet{}, panels)
	_, err := src.Issue(context.Background(), id, "awg3", "vps1")
	var nh *linkrepair.NeedHuman
	if !errors.As(err, &nh) || nh.Action != linkrepair.ActNoSource {
		t.Fatalf("ждали NeedHuman с ActNoSource, получили %v", err)
	}
	if len(panels.routerCalls) != 0 {
		t.Fatalf("панель спрошена с испорченным вариантом: %v", panels.routerCalls)
	}
}

func TestRepairSource_OptionsOrder(t *testing.T) {
	cab := &fakeCabinet{accounts: map[string]VPNAccount{
		"amnezia": {Provider: "amnezia", Connected: true, Options: []VPNOption{{ID: "nl"}, {ID: "de"}, {ID: "fi"}}},
	}}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	got, err := src.Options(context.Background(), id, "amnezia")
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if len(got) != 3 || got[0] != "nl" || got[1] != "de" || got[2] != "fi" {
		t.Fatalf("порядок вариантов: %v", got)
	}
	if opts, err := src.Options(context.Background(), id, "awg3"); err != nil || opts != nil {
		t.Fatalf("у своего сервера вариантов нет: %v %v", opts, err)
	}
}

// Не подключённый кабинет -- смена локации невозможна, нужен ключ.
func TestRepairSource_OptionsNotConnectedNeedsHuman(t *testing.T) {
	src, id := repairSourceEnv(t, &fakeCabinet{}, &fakeAwg3{})
	_, err := src.Options(context.Background(), id, "hidemyname")
	var nh *linkrepair.NeedHuman
	if !errors.As(err, &nh) || nh.Action != linkrepair.ActHideMyCode {
		t.Fatalf("ждали NeedHuman с ActHideMyCode, получили %v", err)
	}
}
