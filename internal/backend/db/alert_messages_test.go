package db

import (
	"path/filepath"
	"testing"
)

func TestAlertMessages_PutListClear(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	router, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg11")
	if err != nil {
		t.Fatal(err)
	}

	if err := d.AlertMessages().Put(router, "tunnel_awg0", 1001, 555); err != nil {
		t.Fatal(err)
	}
	if err := d.AlertMessages().Put(router, "tunnel_awg0", 1002, 777); err != nil {
		t.Fatal(err)
	}

	got, err := d.AlertMessages().List(router, "tunnel_awg0")
	if err != nil {
		t.Fatal(err)
	}
	if got[1001] != 555 || got[1002] != 777 {
		t.Fatalf("карта=%v, ждали у 1001 сообщение 555, у 1002 -- 777", got)
	}

	// Соседняя проверка не смешивается с этой.
	other, err := d.AlertMessages().List(router, "dns")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("чужая проверка=%v, ждали пусто", other)
	}

	// Повторная отправка тому же человеку перезаписывает id, а не плодит строк.
	if err := d.AlertMessages().Put(router, "tunnel_awg0", 1001, 999); err != nil {
		t.Fatal(err)
	}
	got, err = d.AlertMessages().List(router, "tunnel_awg0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1001] != 999 {
		t.Fatalf("карта=%v, ждали перезапись 1001 на 999", got)
	}

	if err := d.AlertMessages().Clear(router, "tunnel_awg0"); err != nil {
		t.Fatal(err)
	}
	got, err = d.AlertMessages().List(router, "tunnel_awg0")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("карта=%v, после Clear ждали пусто", got)
	}
}
