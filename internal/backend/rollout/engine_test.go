package rollout

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func rolloutDB(t *testing.T) (*db.DB, []int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "rollout.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	a, _ := d.Users().Insert("a", "rollout-a", "", "")
	b, _ := d.Users().Insert("b", "rollout-b", "", "")
	return d, []int64{a, b}
}

func TestSelectCanaryPrefersFreshHealthyRouter(t *testing.T) {
	got := SelectCanary([]Candidate{
		{RouterID: 3, Fresh: true, HardIncidents: 1},
		{RouterID: 2, Fresh: false},
		{RouterID: 4, Fresh: true},
	})
	if got != 4 {
		t.Fatalf("canary=%d", got)
	}
}

func TestEngineCanaryObservationThenFleetAndCompletion(t *testing.T) {
	d, ids := rolloutDB(t)
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	probes := map[int64]Probe{}
	deploys := map[int64]int{}
	e := Engine{DB: d, ObservationWindow: 10 * time.Minute, Now: func() time.Time { return now }, Deploy: func(id int64, target string) (string, error) {
		deploys[id]++
		return "cmd-" + string(rune('0'+id)), nil
	}, Probe: func(id int64) Probe { return probes[id] }}
	job, err := e.Create("v1.2.3", 77, ids)
	if err != nil {
		t.Fatal(err)
	}
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "canary_pending" || deploys[ids[0]] != 1 || deploys[ids[1]] != 0 {
		t.Fatalf("job=%+v deploys=%v", job, deploys)
	}
	probes[ids[0]] = Probe{Fresh: true, Version: "v1.2.3"}
	now = now.Add(time.Minute)
	job, _ = e.Tick(job.ID)
	if job.State != "observing" {
		t.Fatalf("job=%+v", job)
	}
	now = now.Add(10 * time.Minute)
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "rolling" || deploys[ids[1]] != 1 {
		t.Fatalf("job=%+v deploys=%v", job, deploys)
	}
	probes[ids[1]] = Probe{Fresh: true, Version: "v1.2.3"}
	now = now.Add(time.Minute)
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "complete" {
		t.Fatalf("job=%+v", job)
	}
}

func TestEnginePausesOnCanaryIncidentAndDoesNotDoubleEnqueueAfterRestart(t *testing.T) {
	d, ids := rolloutDB(t)
	now := time.Now().UTC()
	deploys := 0
	probe := Probe{}
	makeEngine := func() Engine {
		return Engine{DB: d, Now: func() time.Time { return now }, Deploy: func(int64, string) (string, error) { deploys++; return "cmd", nil }, Probe: func(int64) Probe { return probe }}
	}
	e := makeEngine()
	job, err := e.Create("v1", 1, ids)
	if err != nil {
		t.Fatal(err)
	}
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deploys != 1 {
		t.Fatalf("deploys=%d", deploys)
	}
	// A fresh Engine simulates process restart; persisted state must prevent a second enqueue.
	e = makeEngine()
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deploys != 1 {
		t.Fatalf("double enqueue after restart: %d", deploys)
	}
	probe = Probe{Fresh: true, Version: "v1", NewHardIncidents: 1}
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "paused" || job.PauseReason != "new_hard_incident" {
		t.Fatalf("job=%+v", job)
	}
}

func TestEnginePausesWhenRestartFindsUncertainDispatch(t *testing.T) {
	d, ids := rolloutDB(t)
	now := time.Now().UTC()
	deploys := 0
	e := Engine{DB: d, Now: func() time.Time { return now }, Deploy: func(int64, string) (string, error) {
		deploys++
		return "cmd", nil
	}}
	job, err := e.Create("v1", 1, ids)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Rollouts().MarkTargetQueued(job.ID, ids[0], "dispatching", now); err != nil || !ok {
		t.Fatalf("seed uncertain dispatch: ok=%v err=%v", ok, err)
	}
	job, err = e.Tick(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "paused" || job.PauseReason != "dispatch_uncertain" || deploys != 0 {
		t.Fatalf("unsafe restart handling: job=%+v deploys=%d", job, deploys)
	}
}

func TestEngineCancelIsPersistent(t *testing.T) {
	d, ids := rolloutDB(t)
	e := Engine{DB: d, Deploy: func(int64, string) (string, error) { return "cmd", nil }, Probe: func(int64) Probe { return Probe{} }}
	job, err := e.Create("v1", 1, ids)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err := d.Rollouts().Get(job.ID)
	if err != nil || got.State != "cancelled" {
		t.Fatalf("job=%+v err=%v", got, err)
	}
}
