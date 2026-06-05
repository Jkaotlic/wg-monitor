package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")

	in := &State{
		SchemaVersion: 1,
		Backend: BackendState{
			Host:        "1.2.3.4",
			Port:        22,
			User:        "root",
			SourceBind:  "172.16.6.9",
			APIDialHost: "10.0.0.5",
			Domain:      "example.com",
			SSHAuth:     "key",
			KeyPath:     filepath.Join(dir, "id_ed25519"),
		},
		Telegram: TelegramState{
			ChatID:      -1001234567890,
			AdminUserID: 123456789,
		},
		Agents: []AgentState{
			{Nickname: "test", Host: "192.168.1.1", Port: 222, User: "root", Arch: "arm64", ThreadID: 42, Kind: "static"},
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

func TestProfileStatePathUsesIsolatedConfigDir(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", filepath.Join(dir, "roaming"))
	} else {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		t.Setenv("HOME", filepath.Join(dir, "home"))
	}
	got := ProfileStatePath("mobile")
	want := filepath.Join(configRootForTest(), "wg-monitor-deploy", "profiles", "mobile", "wizard.toml")
	if got != want {
		t.Fatalf("ProfileStatePath(mobile)=%q, want %q", got, want)
	}
}

func configRootForTest() string {
	if runtime.GOOS == "windows" {
		return os.Getenv("APPDATA")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), ".config")
	}
	return cfg
}

func TestAgentStateRoundTripMobileRolloutFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wizard.toml")
	in := &State{
		SchemaVersion: 1,
		Agents: []AgentState{{
			Nickname:       "carvan",
			Kind:           "mobile",
			Ring:           "canary",
			PendingVersion: "v0.14.0-rc1",
			PendingSince:   "2026-05-19T10:00:00Z",
		}},
	}
	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip mismatch:\nin:  %+v\nout: %+v", in, out)
	}
}

func TestAgentStateRoundTripAWGMDeployFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wizard.toml")
	in := &State{
		SchemaVersion: CurrentSchemaVersion,
		Agents: []AgentState{{
			Nickname:   "testkeen",
			DeployMode: "awgm",
			AWGMURL:    "https://awg.testkeen.keenetic.pro",
			AWGMAuth:   "router-admin",
		}},
	}
	if err := SaveState(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	ag := out.FindAgent("testkeen")
	if ag == nil {
		t.Fatal("agent missing")
	}
	if ag.DeployMode != "awgm" || ag.AWGMURL != "https://awg.testkeen.keenetic.pro" || ag.AWGMAuth != "router-admin" {
		t.Fatalf("AWGM fields lost: %+v", ag)
	}
}
