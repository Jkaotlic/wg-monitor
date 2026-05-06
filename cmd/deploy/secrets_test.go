package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGetSecret_FromEnv(t *testing.T) {
	t.Setenv("WG_VPS_PASS", "envpass")
	store := NewSecretStore()
	got, src := store.Get("WG_VPS_PASS", "VPS root password", nil)
	if got != "envpass" {
		t.Errorf("got %q want %q", got, "envpass")
	}
	if src != SourceEnv {
		t.Errorf("got source %v want %v", src, SourceEnv)
	}
}

func TestGetSecret_FromMemoryFile(t *testing.T) {
	dir := t.TempDir()
	mem := filepath.Join(dir, "host_keenetic.md")
	os.WriteFile(mem, []byte("router\nuser root\npass MySecret123\nport 222\n"), 0o600)

	t.Setenv("WG_KEENETIC_PASS", "")
	store := NewSecretStore()
	got, src := store.Get("WG_KEENETIC_PASS", "Keenetic root password", &MemoryFileLookup{
		Path:    mem,
		Pattern: `pass\s+([A-Za-z0-9!@#$%^&*_+=\-]+)`,
	})
	if got != "MySecret123" {
		t.Errorf("got %q want %q", got, "MySecret123")
	}
	if src != SourceMemoryFile {
		t.Errorf("got source %v want %v", src, SourceMemoryFile)
	}
}

func TestSecretStore_Trace(t *testing.T) {
	store := NewSecretStore()
	store.recordPrompted("WG_BOT_TOKEN")
	got := store.PromptedSecrets()
	if len(got) != 1 || got[0] != "WG_BOT_TOKEN" {
		t.Errorf("expected [WG_BOT_TOKEN], got %v", got)
	}
}
