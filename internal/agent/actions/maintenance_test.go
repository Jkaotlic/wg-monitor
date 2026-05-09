package actions

import (
	"context"
	"strings"
	"testing"
)

const ndmcComponentsListGolden_NoUpdate = "\x1b[K\n         firmware: \n              version: 5.00.C.11.0-0\n                title: 5.0.11\n\n          sandbox: stable\n\n            local: \n              sandbox: stable\n              version: 5.00.C.11.0-0\n"

const ndmcComponentsListGolden_HasUpdate = "\x1b[K\n         firmware: \n              version: 5.00.C.12.0-0\n                title: 5.0.12\n\n          sandbox: stable\n\n            local: \n              sandbox: stable\n              version: 5.00.C.11.0-0\n"

func TestParseComponentsList_NoUpdate(t *testing.T) {
	fs, err := parseComponentsList(ndmcComponentsListGolden_NoUpdate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "" {
		t.Errorf("Available=%q, expected empty when current==firmware", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_HasUpdate(t *testing.T) {
	fs, err := parseComponentsList(ndmcComponentsListGolden_HasUpdate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "5.00.C.12.0-0" {
		t.Errorf("Available=%q, want 5.00.C.12.0-0", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_CRLF(t *testing.T) {
	// Same content as NoUpdate golden but with CRLF line endings, as produced by
	// some SSH wrappers or Windows test fixtures.
	crlf := strings.ReplaceAll(ndmcComponentsListGolden_NoUpdate, "\n", "\r\n")
	fs, err := parseComponentsList(crlf)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Current != "5.00.C.11.0-0" {
		t.Errorf("Current=%q, want 5.00.C.11.0-0", fs.Current)
	}
	if fs.Available != "" {
		t.Errorf("Available=%q, expected empty when current==firmware", fs.Available)
	}
	if fs.Channel != "stable" {
		t.Errorf("Channel=%q, want stable", fs.Channel)
	}
}

func TestParseComponentsList_MissingLocal(t *testing.T) {
	in := "         firmware: \n              version: 5.00.C.11.0-0\n          sandbox: stable\n"
	if _, err := parseComponentsList(in); err == nil {
		t.Error("expected error when local block is missing")
	}
}

func TestGetFirmwareStatus_ExecError(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, &execErr{msg: "boom"}
	}
	if _, err := GetFirmwareStatus(context.Background(), exec); err == nil {
		t.Fatal("expected error from exec failure")
	}
}

type execErr struct{ msg string }

func (e *execErr) Error() string { return e.msg }

func TestInstallFirmware_ExecCommand(t *testing.T) {
	var got [][]string
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		got = append(got, append([]string{name}, args...))
		return []byte("ok"), nil
	}
	if err := InstallFirmware(context.Background(), exec); err != nil {
		t.Fatalf("InstallFirmware: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(got))
	}
	want := []string{"ndmc", "-c", "components commit"}
	if !slicesEq(got[0], want) {
		t.Errorf("exec=%v, want %v", got[0], want)
	}
}

func TestInstallFirmware_ExecError(t *testing.T) {
	exec := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return nil, &execErr{msg: "no such command"}
	}
	if err := InstallFirmware(context.Background(), exec); err == nil {
		t.Error("expected error from exec failure")
	}
}

func slicesEq(a, b []string) bool {
	if len(a) != len(b) { return false }
	for i := range a {
		if a[i] != b[i] { return false }
	}
	return true
}
