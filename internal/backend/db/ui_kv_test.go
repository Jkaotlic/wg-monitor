package db

import (
	"strings"
	"testing"
)

func TestKVTopicIDRoundTrip(t *testing.T) {
	d := openTempDB(t)
	if err := d.KV().SetTopicID("summary", 7); err != nil {
		t.Fatal(err)
	}
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil || !ok || id != 7 {
		t.Fatalf("got id=%d ok=%v err=%v want 7,true,nil", id, ok, err)
	}
	if err := d.KV().SetTopicID("systemic", 99); err != nil {
		t.Fatal(err)
	}
	id, ok, _ = d.KV().GetTopicID("systemic")
	if id != 99 || !ok {
		t.Fatalf("systemic got %d ok=%v want 99,true", id, ok)
	}
}

func TestKVTopicIDMiss(t *testing.T) {
	d := openTempDB(t)
	id, ok, err := d.KV().GetTopicID("summary")
	if err != nil {
		t.Fatal(err)
	}
	if ok || id != 0 {
		t.Fatalf("got id=%d ok=%v want 0,false", id, ok)
	}
}

func TestKVTopicIDInvalidKind(t *testing.T) {
	d := openTempDB(t)
	err := d.KV().SetTopicID("garbage", 1)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("expected kind-validation error, got %v", err)
	}
	if _, _, err := d.KV().GetTopicID("garbage"); err == nil {
		t.Fatalf("expected kind-validation error from Get, got nil")
	}
}
