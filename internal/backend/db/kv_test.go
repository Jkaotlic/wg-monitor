package db

import "testing"

func TestKVSetGet(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	d, err := Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := d.KV().Set("last_update_id", "12345"); err != nil {
		t.Fatal(err)
	}
	v, err := d.KV().Get("last_update_id")
	if err != nil {
		t.Fatal(err)
	}
	if v != "12345" {
		t.Errorf("got %q, want %q", v, "12345")
	}
}

func TestKVGetMissing(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	d, _ := Open(tmp)
	defer d.Close()
	v, err := d.KV().Get("nope")
	if err != nil {
		t.Fatal(err)
	}
	if v != "" {
		t.Errorf("missing key should return empty string, got %q", v)
	}
}

func TestKVOverwrite(t *testing.T) {
	tmp := t.TempDir() + "/test.db"
	d, _ := Open(tmp)
	defer d.Close()
	_ = d.KV().Set("k", "a")
	_ = d.KV().Set("k", "b")
	v, _ := d.KV().Get("k")
	if v != "b" {
		t.Errorf("overwrite failed, got %q", v)
	}
}
