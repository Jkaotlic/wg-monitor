package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/db"
)

func TestRunSetTopic_Summary(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	var out bytes.Buffer
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "summary", ThreadID: 11, Out: &out}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "summary") || !strings.Contains(out.String(), "11") {
		t.Errorf("out: %s", out.String())
	}
	d, _ = db.Open(dbPath)
	defer d.Close()
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil || !ok || id != 11 {
		t.Errorf("kv: id=%d ok=%v err=%v", id, ok, err)
	}
}

func TestRunSetTopic_Systemic(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "systemic", ThreadID: 22, Out: &out}); err != nil {
		t.Fatal(err)
	}
	d, _ = db.Open(dbPath)
	defer d.Close()
	id, ok, _ := d.KV().GetTopicID("systemic")
	if !ok || id != 22 {
		t.Errorf("systemic kv: id=%d ok=%v", id, ok)
	}
}

func TestRunSetTopic_InvalidKind(t *testing.T) {
	dbPath := t.TempDir() + "/x.db"
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "garbage", ThreadID: 1, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("expected kind error, got %v", err)
	}
}

func TestRunSetTopic_MissingThreadID(t *testing.T) {
	var out bytes.Buffer
	err := runSetTopic(setTopicOpts{DBPath: t.TempDir() + "/x.db", Kind: "summary", ThreadID: 0, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "thread") {
		t.Errorf("expected thread-id error, got %v", err)
	}
}

func TestRunSetTopic_OverwriteExisting(t *testing.T) {
	// Set summary=11, then summary=33. The second write must replace the first
	// (KV upsert), not silently noop or duplicate-row-error.
	dbPath := t.TempDir() + "/x.db"
	d, _ := db.Open(dbPath)
	d.Close()
	var out bytes.Buffer
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "summary", ThreadID: 11, Out: &out}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runSetTopic(setTopicOpts{DBPath: dbPath, Kind: "summary", ThreadID: 33, Out: &out}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	d, _ = db.Open(dbPath)
	defer d.Close()
	id, _, _ := d.KV().GetTopicID("summary")
	if id != 33 {
		t.Errorf("overwrite failed: got id=%d, want 33", id)
	}
}
