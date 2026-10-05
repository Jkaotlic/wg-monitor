package db

import (
	"testing"
	"time"
)

func TestTunnelRepairSettings_MissingIsOff(t *testing.T) {
	d, userID := originDB(t)
	_, ok, err := d.TunnelRepairSettings().Get(userID, "awg12")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("строки нет -- ok должно быть false")
	}
}

func TestTunnelRepairSettings_PutGetRoundTrip(t *testing.T) {
	d, userID := originDB(t)
	in := TunnelRepairSetting{UserID: userID, TunnelID: "awg12", TunnelName: "Дача", Enabled: true, Provider: "amnezia", Option: "nl", AllowRelocate: true, UpdatedBy: 42}
	before := time.Now().UTC().Add(-time.Minute)
	if err := d.TunnelRepairSettings().Put(in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.TunnelRepairSettings().Get(userID, "awg12")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.UserID != userID || got.TunnelID != "awg12" || !got.Enabled || got.Provider != "amnezia" ||
		got.Option != "nl" || !got.AllowRelocate || got.UpdatedBy != 42 || got.TunnelName != "Дача" {
		t.Fatalf("setting = %+v", got)
	}
	if got.UpdatedAt.Before(before) {
		t.Fatalf("updated_at = %v", got.UpdatedAt)
	}
}

func TestTunnelRepairSettings_PutOverwrites(t *testing.T) {
	d, userID := originDB(t)
	r := d.TunnelRepairSettings()
	if err := r.Put(TunnelRepairSetting{UserID: userID, TunnelID: "awg12", Enabled: true, Provider: "amnezia"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(TunnelRepairSetting{UserID: userID, TunnelID: "awg12", Enabled: false, Provider: "awg3", UpdatedBy: 7}); err != nil {
		t.Fatal(err)
	}
	got, _, err := r.Get(userID, "awg12")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.Provider != "awg3" || got.UpdatedBy != 7 {
		t.Fatalf("setting = %+v", got)
	}
}

func TestTunnelRepairSettings_ListOnlyEnabled(t *testing.T) {
	d, userID := originDB(t)
	r := d.TunnelRepairSettings()
	for _, s := range []TunnelRepairSetting{
		{UserID: userID, TunnelID: "awg13", Enabled: true},
		{UserID: userID, TunnelID: "awg11", Enabled: false},
		{UserID: userID, TunnelID: "awg12", Enabled: true},
	} {
		if err := r.Put(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := r.List(userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TunnelID != "awg12" || got[1].TunnelID != "awg13" {
		t.Fatalf("list = %+v", got)
	}
}

func TestTunnelRepairSettings_CascadeOnRouterDelete(t *testing.T) {
	d, userID := originDB(t)
	r := d.TunnelRepairSettings()
	if err := r.Put(TunnelRepairSetting{UserID: userID, TunnelID: "awg12", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		t.Fatal(err)
	}
	_, ok, err := r.Get(userID, "awg12")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("строки должны уйти вместе с пользователем")
	}
}

// Delete -- VPN-туннель удалён или списан заменой: его id awg-manager может
// отдать новому туннелю, и настройка не должна к нему перейти.
func TestTunnelRepairSettings_Delete(t *testing.T) {
	d, userID := originDB(t)
	r := d.TunnelRepairSettings()
	for _, id := range []string{"awg12", "awg13"} {
		if err := r.Put(TunnelRepairSetting{UserID: userID, TunnelID: id, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Delete(userID, "awg12"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.Get(userID, "awg12"); ok {
		t.Fatal("строка awg12 осталась")
	}
	if _, ok, _ := r.Get(userID, "awg13"); !ok {
		t.Fatal("удалена чужая строка awg13")
	}
	if err := r.Delete(userID, "awg99"); err != nil {
		t.Fatalf("удаление несуществующей строки -- не ошибка: %v", err)
	}
}
