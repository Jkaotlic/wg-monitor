package db

import (
	"path/filepath"
	"testing"
	"time"
)

func originDB(t *testing.T) (*DB, int64) {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	id, err := d.Users().Insert("router", "tok-0000000000000000000000000000000000000000000000000000000000", "1.1.1.1", "awg1")
	if err != nil {
		t.Fatal(err)
	}
	return d, id
}

func TestTunnelOrigin_RecordAndGet(t *testing.T) {
	d, userID := originDB(t)
	when := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	if err := d.TunnelOrigins().Record(userID, "awg21", "amnezia_nl", "amnezia", "nl", when, 42); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.TunnelOrigins().Get(userID, "awg21")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Provider != "amnezia" || got.Variant != "nl" || got.TunnelName != "amnezia_nl" || got.IssuedBy != 42 {
		t.Fatalf("origin = %+v", got)
	}
}

// Туннель, заведённый руками, происхождения не имеет -- и это ответ, а не
// ошибка: выдумывать провайдера за него нечем.
func TestTunnelOrigin_UnknownIsAnAnswer(t *testing.T) {
	d, userID := originDB(t)
	_, ok, err := d.TunnelOrigins().Get(userID, "awg11")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("происхождения быть не должно")
	}
}

// Перевыпуск перезаписывает строку: актуально то, чем туннель поднят сейчас.
func TestTunnelOrigin_ReissueOverwrites(t *testing.T) {
	d, userID := originDB(t)
	first := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	second := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if err := d.TunnelOrigins().Record(userID, "awg21", "amnezia_nl", "amnezia", "nl", first, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.TunnelOrigins().Record(userID, "awg21", "amnezia_se", "amnezia", "se", second, 2); err != nil {
		t.Fatal(err)
	}
	got, _, err := d.TunnelOrigins().Get(userID, "awg21")
	if err != nil {
		t.Fatal(err)
	}
	if got.Variant != "se" || !got.IssuedAt.Equal(second) {
		t.Fatalf("origin = %+v", got)
	}
	list, err := d.TunnelOrigins().List(userID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
}

// Удалённый VPN-туннель не оставляет следов: ни происхождения, ни отметки
// «не подтверждена». Чужие строки и другие роутеры не трогаются.
func TestTunnelOrigin_DeleteDropsOriginAndUnconfirmed(t *testing.T) {
	d, userID := originDB(t)
	other, err := d.Users().Insert("router2", "tok-1111111111111111111111111111111111111111111111111111111111", "2.2.2.2", "awg1")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, id := range []int64{userID, other} {
		if err := d.TunnelOrigins().RecordUnconfirmed(id, "awg21", "amnezia_nl", "amnezia", "nl", when); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.TunnelOrigins().Record(userID, "awg22", "amnezia_de", "amnezia", "de", when, 1); err != nil {
		t.Fatal(err)
	}
	if err := d.TunnelOrigins().Delete(userID, "awg21"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.TunnelOrigins().Get(userID, "awg21"); ok {
		t.Fatal("происхождение удалённого туннеля осталось")
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM tunnel_origin_unconfirmed WHERE user_id = ? AND tunnel_id = 'awg21'`, userID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("отметка «не подтверждена» осталась: n=%d err=%v", n, err)
	}
	if _, ok, _ := d.TunnelOrigins().Get(userID, "awg22"); !ok {
		t.Fatal("чужой туннель того же роутера потерян")
	}
	if _, ok, _ := d.TunnelOrigins().Get(other, "awg21"); !ok {
		t.Fatal("тот же id у другого роутера потерян")
	}
	// повторное удаление безопасно
	if err := d.TunnelOrigins().Delete(userID, "awg21"); err != nil {
		t.Fatal(err)
	}
}
