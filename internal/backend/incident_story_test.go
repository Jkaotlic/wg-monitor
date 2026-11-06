package backend

import (
	"testing"
	"time"
)

func TestIncidentStoryFoldsFailureFailoverRepairAndRecovery(t *testing.T) {
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	events := []incidentStoryEvent{
		{At: base, Kind: "check", Status: "fail"},
		{At: base.Add(time.Minute), Kind: "failover", Status: "ok", CommandID: "cmd-promote"},
		{At: base.Add(2 * time.Minute), Kind: "repair", Status: "started", CommandID: "repair-7"},
		{At: base.Add(3 * time.Minute), Kind: "repair", Status: "ok", CommandID: "repair-7"},
		{At: base.Add(4 * time.Minute), Kind: "check", Status: "ok"},
	}

	got := foldIncidentStory(events)
	wantKinds := []string{"failure_detected", "failover_completed", "repair_started", "repair_completed", "recovered"}
	if len(got) != len(wantKinds) {
		t.Fatalf("steps = %+v", got)
	}
	for i, kind := range wantKinds {
		if got[i].Kind != kind {
			t.Fatalf("step %d kind = %q, want %q; all=%+v", i, got[i].Kind, kind, got)
		}
	}
	if got[1].CommandID != "cmd-promote" || got[3].CommandID != "repair-7" {
		t.Fatalf("command correlation lost: %+v", got)
	}
}

func TestIncidentStoryKeepsPartialSequenceHonest(t *testing.T) {
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	got := foldIncidentStory([]incidentStoryEvent{
		{At: base, Kind: "check", Status: "fail"},
		{At: base.Add(time.Minute), Kind: "repair", Status: "failed", Detail: "ssh://secret@example.invalid token=secret"},
	})

	if len(got) != 2 || got[1].Kind != "repair_failed" || got[1].Tone != "danger" {
		t.Fatalf("steps = %+v", got)
	}
	if got[1].Detail != "Автоматическая починка не завершилась" {
		t.Fatalf("unsafe or unstable detail leaked: %q", got[1].Detail)
	}
}

func TestIncidentStoryCompressesDuplicateChecks(t *testing.T) {
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	got := foldIncidentStory([]incidentStoryEvent{
		{At: base, Kind: "check", Status: "fail"},
		{At: base.Add(time.Minute), Kind: "check", Status: "fail"},
		{At: base.Add(2 * time.Minute), Kind: "check", Status: "ok"},
	})
	if len(got) != 2 || got[0].Kind != "failure_detected" || got[1].Kind != "recovered" {
		t.Fatalf("steps = %+v", got)
	}
}
