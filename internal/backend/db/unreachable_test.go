package db

import (
	"path/filepath"
	"testing"
)

func TestUnreachable_MarkListClear(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := d.Unreachable().Mark(1001, "bot can't initiate conversation"); err != nil {
		t.Fatal(err)
	}
	got, err := d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TelegramUserID != 1001 {
		t.Fatalf("список=%v, ждали 1001", got)
	}
	if got[0].LastError == "" {
		t.Fatal("причина обязана сохраниться: без неё оператор не поймёт, что чинить")
	}

	// Повторная пометка не плодит строк.
	if err := d.Unreachable().Mark(1001, "bot was blocked by the user"); err != nil {
		t.Fatal(err)
	}
	got, err = d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("строк=%d, повторная пометка не должна дублировать", len(got))
	}

	// Человек подхватился -- отметка снимается.
	if err := d.Unreachable().Clear(1001); err != nil {
		t.Fatal(err)
	}
	got, err = d.Unreachable().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("список=%v, ждали пусто после успешной доставки", got)
	}
}
