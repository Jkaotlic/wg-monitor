package actions

import (
	"context"
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
