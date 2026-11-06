package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRolloutJobsPersistTargetsAndResumeState(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "rollout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	first, _ := d.Users().Insert("canary", "token-rollout-1", "", "")
	second, _ := d.Users().Insert("rest", "token-rollout-2", "", "")
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	job := RolloutJob{ID: "rollout-1", TargetVersion: "v1.2.3", State: "planned", CreatedBy: 77, CreatedAt: now, UpdatedAt: now}
	if err := d.Rollouts().Create(job, []int64{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := d.Rollouts().Transition("rollout-1", "planned", "canary_pending", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, targets, err := d.Rollouts().Get("rollout-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "canary_pending" || len(targets) != 2 || targets[0].RouterID != first || targets[0].Role != "canary" || targets[1].Role != "fleet" {
		t.Fatalf("job=%+v targets=%+v", got, targets)
	}
	// Re-open proves the state is not process memory.
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = Open(filepath.Join(filepath.Dir(d.Path()), "rollout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, _, err = d.Rollouts().Get("rollout-1")
	if err != nil || got.State != "canary_pending" {
		t.Fatalf("resumed job=%+v err=%v", got, err)
	}
}

func TestRolloutTargetCanOnlyQueueOnce(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "rollout.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	uid, _ := d.Users().Insert("canary", "token-rollout-once", "", "")
	now := time.Now().UTC()
	if err := d.Rollouts().Create(RolloutJob{ID: "r", TargetVersion: "v1", State: "planned", CreatedAt: now, UpdatedAt: now}, []int64{uid}); err != nil {
		t.Fatal(err)
	}
	queued, err := d.Rollouts().MarkTargetQueued("r", uid, "cmd-1", now)
	if err != nil || !queued {
		t.Fatalf("first queued=%v err=%v", queued, err)
	}
	queued, err = d.Rollouts().MarkTargetQueued("r", uid, "cmd-2", now)
	if err != nil || queued {
		t.Fatalf("second queued=%v err=%v", queued, err)
	}
}
