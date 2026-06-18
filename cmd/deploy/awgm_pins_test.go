package main

import (
	"path/filepath"
	"testing"
)

func TestAWGMPinStore_TOFUThenEnforce(t *testing.T) {
	store := &awgmPinStore{path: filepath.Join(t.TempDir(), "awgm_pins")}

	// First contact: accept and record (TOFU).
	if err := store.verify("awg.example", []byte("certA")); err != nil {
		t.Fatalf("first verify should TOFU-accept: %v", err)
	}
	// Same cert later: accept.
	if err := store.verify("awg.example", []byte("certA")); err != nil {
		t.Fatalf("matching cert should accept: %v", err)
	}
	// Different cert for the same host: reject (possible MITM).
	if err := store.verify("awg.example", []byte("certB")); err == nil {
		t.Fatal("changed cert for pinned host must be rejected")
	}
	// A different host pins independently.
	if err := store.verify("awg.other", []byte("certB")); err != nil {
		t.Fatalf("new host should TOFU-accept independently: %v", err)
	}
}

func TestAWGMPinStore_PersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "awgm_pins")
	first := &awgmPinStore{path: path}
	if err := first.verify("awg.example", []byte("certA")); err != nil {
		t.Fatalf("seed pin: %v", err)
	}
	// A fresh store reading the same file must enforce the recorded pin.
	second := &awgmPinStore{path: path}
	if err := second.verify("awg.example", []byte("certZ")); err == nil {
		t.Fatal("reloaded pin store must reject a changed cert")
	}
	if err := second.verify("awg.example", []byte("certA")); err != nil {
		t.Fatalf("reloaded pin store must accept the original cert: %v", err)
	}
}

func TestAWGMHost(t *testing.T) {
	cases := map[string]string{
		"https://awg.example/":          "awg.example",
		"https://AWG.Example:2222/x":     "awg.example",
		"awg.example":                    "awg.example",
	}
	for in, want := range cases {
		if got := awgmHost(in); got != want {
			t.Fatalf("awgmHost(%q)=%q, want %q", in, got, want)
		}
	}
}
