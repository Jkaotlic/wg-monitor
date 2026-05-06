package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")

	in := &State{
		SchemaVersion: 1,
		Backend: BackendState{
			Host:   "1.2.3.4",
			Port:   22,
			User:   "root",
			Domain: "example.com",
		},
		Telegram: TelegramState{
			ChatID:      -1001234567890,
			AdminUserID: 123456789,
		},
		Agents: []AgentState{
			{Nickname: "test", Host: "192.168.1.1", Port: 222, User: "root", Arch: "arm64", ThreadID: 42, AwgIface: "awg0", ExpectedExitIP: "1.2.3.4"},
		},
	}

	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}

	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestLoadState_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope.toml")
	s, err := LoadState(path)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if s == nil {
		t.Fatal("expected default state, got nil")
	}
	if s.SchemaVersion != 1 {
		t.Errorf("expected default SchemaVersion=1, got %d", s.SchemaVersion)
	}
}

func TestLoadState_FutureSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")
	if err := os.WriteFile(path, []byte("schema_version = 999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadState(path)
	if err == nil {
		t.Fatal("expected error for unsupported schema_version")
	}
}
