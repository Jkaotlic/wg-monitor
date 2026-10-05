package backend

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
		"amnezia": {Provider: "amnezia", Connected: true, Options: []VPNOption{{ID: "nl", Label: "Нидерланды", Issued: true}, {ID: "de", Label: "Германия"}, {ID: "fi", Label: "Финляндия", Issued: true}}},
	}}
	src, id := repairSourceEnv(t, cab, &fakeAwg3{})
	got, err := src.Options(context.Background(), id, "amnezia")
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	// Подпись и «уже выпущен» доходят до движка: по ним он решает, что можно
	// пробовать без нового места в подписке, и как назвать локацию человеку.
	want := []linkrepair.Option{{ID: "nl", Label: "Нидерланды", Issued: true}, {ID: "de", Label: "Германия"}, {ID: "fi", Label: "Финляндия", Issued: true}}
	if len(got) != len(want) {
		t.Fatalf("варианты: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("вариант %d = %+v, ждали %+v", i, got[i], want[i])
		}
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

func repairDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("дача", "tok-repair-wiring", "", "")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return d, id
}

// Движок видит настройку туннеля из таблицы; строки нет -- выключено.
func TestLinkRepairSettings_ReadsTable(t *testing.T) {
	d, id := repairDB(t)
	get := LinkRepairSettings(d, nil)
	if _, ok := get(id, "awg12"); ok {
		t.Fatal("строки нет -- настройки нет")
	}
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{
		UserID: id, TunnelID: "awg12", TunnelName: "Дача", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true, UpdatedBy: 77,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := d.TunnelRepairSettings().SpendRelocation(id, "awg12", "de"); err != nil {
		t.Fatalf("SpendRelocation: %v", err)
	}
	got, ok := get(id, "awg12")
	want := linkrepair.Setting{Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true, TunnelName: "Дача", RelocateSpent: "de"}
	if !ok || got != want {
		t.Fatalf("настройка %+v ok=%v, ждали %+v", got, ok, want)
	}
}

// Удачная смена локации: новый вариант -- в настройку туннеля (остальное не
// трогается) и в происхождение конфига.
func TestLinkRepairSaveOption_UpdatesSettingAndOrigin(t *testing.T) {
	d, id := repairDB(t)
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{
		UserID: id, TunnelID: "awg12", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true, UpdatedBy: 77,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	LinkRepairSaveOption(d, nil)(id, "awg12", "amnezia", "de")

	s, ok, err := d.TunnelRepairSettings().Get(id, "awg12")
	if err != nil || !ok {
		t.Fatalf("Get: %v %v", ok, err)
	}
	if s.Option != "de" || !s.Enabled || s.Provider != "amnezia" || !s.AllowRelocate || s.UpdatedBy != 77 {
		t.Fatalf("настройка после смены локации: %+v", s)
	}
	o, ok, err := d.TunnelOrigins().Get(id, "awg12")
	if err != nil || !ok || o.Provider != "amnezia" || o.Variant != "de" {
		t.Fatalf("происхождение: %+v ok=%v err=%v", o, ok, err)
	}
}

// Настройку успели выключить, пока шла починка: вариант в происхождение
// пишется (конфиг на роутере уже новый), а тумблер не включается обратно.
func TestLinkRepairSaveOption_DoesNotReenable(t *testing.T) {
	d, id := repairDB(t)
	LinkRepairSaveOption(d, nil)(id, "awg12", "amnezia", "de")
	if _, ok, _ := d.TunnelRepairSettings().Get(id, "awg12"); ok {
		t.Fatal("строки настройки не было -- и не появилось")
	}
	if o, ok, _ := d.TunnelOrigins().Get(id, "awg12"); !ok || o.Variant != "de" {
		t.Fatalf("происхождение: %+v %v", o, ok)
	}
}

// Строка «Автопочинка включена» в тревоге -- только у VPN-туннеля с
// включённой настройкой; у прочих проверок её не бывает.
func TestAutoRepairHint(t *testing.T) {
	d, id := repairDB(t)
	hint := AutoRepairHint(d)
	if hint(id, "tunnel_awg12") {
		t.Fatal("строки нет -- автопочинка выключена")
	}
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: id, TunnelID: "awg12", Enabled: true}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !hint(id, "tunnel_awg12") {
		t.Fatal("включённая автопочинка не видна")
	}
	if hint(id, "awg12") || hint(id, "dns") || hint(id, "tunnel_awg13") {
		t.Fatal("подсказка у чужой проверки")
	}
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: id, TunnelID: "awg12", Enabled: false}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if hint(id, "tunnel_awg12") {
		t.Fatal("выключенная автопочинка видна как включённая")
	}
}

// Настройка удалённого или списанного заменой VPN-туннеля удаляется.
func TestLinkRepairDropSetting(t *testing.T) {
	d, id := repairDB(t)
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: id, TunnelID: "awg12", Enabled: true}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	LinkRepairDropSetting(d, nil)(id, "awg12")
	if _, ok, _ := d.TunnelRepairSettings().Get(id, "awg12"); ok {
		t.Fatal("настройка осталась")
	}
}

// Отметка о новой стране пишется в настройку; вторая -- ошибка, и движок
// тогда страну не выпускает.
func TestLinkRepairSpendRelocation(t *testing.T) {
	d, id := repairDB(t)
	spend := LinkRepairSpendRelocation(d)
	if err := spend(id, "awg12", "de"); err == nil {
		t.Fatal("настройки нет -- ждали ошибку")
	}
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{UserID: id, TunnelID: "awg12", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := spend(id, "awg12", "de"); err != nil {
		t.Fatalf("spend: %v", err)
	}
	if err := spend(id, "awg12", "se"); err == nil {
		t.Fatal("вторая отметка -- ждали ошибку")
	}
	if s, _, _ := d.TunnelRepairSettings().Get(id, "awg12"); s.RelocateSpent != "de" {
		t.Fatalf("отметка: %+v", s)
	}
}

// A4.6: место в подписке «Amnezia Premium» -- по счёту устройств кабинета.
func TestRepairSource_HasRoom(t *testing.T) {
	cases := []struct {
		name      string
		used, max int
		want      bool
	}{
		{"есть место", 2, 3, true},
		{"заполнена", 3, 3, false},
		{"без предела", 5, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cab := &fakeCabinet{accounts: map[string]VPNAccount{
				"amnezia": {Provider: "amnezia", Connected: true, DevicesUsed: c.used, DevicesMax: c.max},
			}}
			src, id := repairSourceEnv(t, cab, &fakeAwg3{})
			got, err := src.HasRoom(context.Background(), id, "amnezia")
			if err != nil || got != c.want {
				t.Fatalf("HasRoom=%v err=%v, ждали %v", got, err, c.want)
			}
		})
	}
	// Кабинет не подключён или не ответил -- не «место есть», а человек.
	src, id := repairSourceEnv(t, &fakeCabinet{accounts: map[string]VPNAccount{}}, &fakeAwg3{})
	var nh *linkrepair.NeedHuman
	if _, err := src.HasRoom(context.Background(), id, "amnezia"); !errors.As(err, &nh) || nh.Action != linkrepair.ActAmneziaKey {
		t.Fatalf("неподключённый кабинет: %v", err)
	}
	src, id = repairSourceEnv(t, &fakeCabinet{err: errors.New("таймаут")}, &fakeAwg3{})
	if _, err := src.HasRoom(context.Background(), id, "amnezia"); !errors.As(err, &nh) {
		t.Fatalf("кабинет не ответил: %v", err)
	}
}

// A4.3: другая страна легла на роутер, проверку не прошла -- настройка и
// происхождение указывают на неё, с отметкой «не подтверждена»; удачная
// запись потом отметку снимает.
func TestLinkRepairSaveUnconfirmed(t *testing.T) {
	d, id := repairDB(t)
	if err := d.TunnelRepairSettings().Put(db.TunnelRepairSetting{
		UserID: id, TunnelID: "awg12", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true,
	}); err != nil {
		t.Fatal(err)
	}
	LinkRepairSaveUnconfirmed(d, nil)(id, "awg12", "amnezia", "de")

	s, _, _ := d.TunnelRepairSettings().Get(id, "awg12")
	if s.Option != "de" || !s.Enabled {
		t.Fatalf("настройка не пошла за роутером: %+v", s)
	}
	o, ok, err := d.TunnelOrigins().Get(id, "awg12")
	if err != nil || !ok || o.Variant != "de" || !o.Unconfirmed {
		t.Fatalf("происхождение: %+v ok=%v err=%v", o, ok, err)
	}

	LinkRepairSaveOption(d, nil)(id, "awg12", "amnezia", "de")
	if o, _, _ := d.TunnelOrigins().Get(id, "awg12"); o.Unconfirmed {
		t.Fatalf("подтверждённая запись не сняла отметку: %+v", o)
	}
}

// Отказ панели своего сервера: человеку -- общее действие без причины
// (пароль, сертификат, пауза -- дело админа), причина -- только в Cause для
// журнала.
func TestRepairSource_Awg3PanelCauseNotInAction(t *testing.T) {
	const secret = "пароль панели не принят — пересохраните учётные данные"
	panels := &fakeAwg3{routerErr: &awg3panel.Error{Kind: awg3panel.KindBadPassword, Msg: secret}}
	src, id := repairSourceEnv(t, &fakeCabinet{}, panels)
	_, err := src.Issue(context.Background(), id, "awg3", "main/awg1")
	var nh *linkrepair.NeedHuman
	if !errors.As(err, &nh) {
		t.Fatalf("ждали NeedHuman, получили %v", err)
	}
	if strings.Contains(nh.Action, "пароль") || strings.Contains(nh.Action, "пересохраните") {
		t.Fatalf("причина панели в действии для владельца: %q", nh.Action)
	}
}
