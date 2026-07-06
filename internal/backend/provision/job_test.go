package provision

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

// --- (a) Create / Get / copy-isolation ---------------------------------

func TestStore_CreateReturnsRunningJobWithPendingSteps(t *testing.T) {
	s := NewStore()
	steps := []Step{
		{Name: "terminal_connected", Status: StepPending},
		{Name: "downloading", Status: StepPending},
	}

	job := s.Create(KindProvision, "router1", steps)

	if job.ID == "" {
		t.Fatal("ID is empty")
	}
	if job.Nickname != "router1" {
		t.Errorf("Nickname = %q, want %q", job.Nickname, "router1")
	}
	if job.Kind != KindProvision {
		t.Errorf("Kind = %q, want %q", job.Kind, KindProvision)
	}
	if job.State != StateRunning {
		t.Errorf("State = %q, want %q", job.State, StateRunning)
	}
	if len(job.Steps) != 2 {
		t.Fatalf("len(Steps) = %d, want 2", len(job.Steps))
	}
	for i, st := range job.Steps {
		if st.Status != StepPending {
			t.Errorf("Steps[%d].Status = %q, want %q", i, st.Status, StepPending)
		}
	}
}

func TestStore_GetReturnsEqualCopy(t *testing.T) {
	s := NewStore()
	job := s.Create(KindRegister, "router1", []Step{{Name: "x", Status: StepPending}})

	got, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if !reflect.DeepEqual(got, *job) {
		t.Fatalf("Get copy differs from Create result:\n got=%+v\nwant=%+v", got, *job)
	}
}

func TestStore_MutatingGetCopyDoesNotChangeStoredJob(t *testing.T) {
	s := NewStore()
	job := s.Create(KindRegister, "router1", []Step{{Name: "x", Status: StepPending}})

	got, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}

	// Mutate the returned copy's step and top-level field.
	got.Steps[0].Status = StepDone
	got.Nickname = "mutated"

	again, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("Get (second): not found")
	}
	if again.Steps[0].Status != StepPending {
		t.Errorf("stored Steps[0].Status leaked mutation: got %q, want %q", again.Steps[0].Status, StepPending)
	}
	if again.Nickname != "router1" {
		t.Errorf("stored Nickname leaked mutation: got %q, want %q", again.Nickname, "router1")
	}
}

func TestStore_MutatingCreateReturnedJobDoesNotChangeStoredJob(t *testing.T) {
	s := NewStore()
	job := s.Create(KindRegister, "router1", []Step{{Name: "x", Status: StepPending}})

	// Create's return value must also be a private copy, not the pointer the
	// store holds internally: the only sanctioned mutation path is Update.
	job.Steps[0].Status = StepFailed
	job.Nickname = "mutated"

	got, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.Steps[0].Status != StepPending {
		t.Errorf("stored Steps[0].Status leaked mutation via Create's pointer: got %q", got.Steps[0].Status)
	}
	if got.Nickname != "router1" {
		t.Errorf("stored Nickname leaked mutation via Create's pointer: got %q", got.Nickname)
	}
}

func TestStore_CreateDoesNotShareStepsBackingArrayAcrossJobs(t *testing.T) {
	s := NewStore()
	template := []Step{{Name: "a", Status: StepPending}}

	job1 := s.Create(KindProvision, "r1", template)
	job2 := s.Create(KindProvision, "r2", template)

	// Mutate job1's returned Steps: job2 (built from the same template slice)
	// must be unaffected.
	job1.Steps[0].Status = StepDone
	got2, ok := s.Get(job2.ID)
	if !ok {
		t.Fatal("Get job2: not found")
	}
	if got2.Steps[0].Status != StepPending {
		t.Errorf("job2 leaked job1's mutation via a shared backing array: got %q", got2.Steps[0].Status)
	}

	// Mutating the caller's original template slice after Create must not
	// reach into either stored job (deep copy at Create time, not an alias).
	template[0].Status = StepFailed
	got1, ok := s.Get(job1.ID)
	if !ok {
		t.Fatal("Get job1: not found")
	}
	if got1.Steps[0].Status == StepFailed {
		t.Errorf("stored job1 aliases the caller's template slice: got %q", got1.Steps[0].Status)
	}
}

func TestStore_UpdateMutatesStoredJobUnderLock(t *testing.T) {
	s := NewStore()
	job := s.Create(KindRepairRepoint, "router1", []Step{{Name: "x", Status: StepPending}})

	s.Update(job.ID, func(j *Job) {
		j.Steps[0].Status = StepActive
		j.Hint = "working"
	})

	got, ok := s.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.Steps[0].Status != StepActive {
		t.Errorf("Steps[0].Status = %q, want %q", got.Steps[0].Status, StepActive)
	}
	if got.Hint != "working" {
		t.Errorf("Hint = %q, want %q", got.Hint, "working")
	}
	if got.State != StateRunning {
		t.Errorf("State = %q, want unchanged %q", got.State, StateRunning)
	}
}

func TestStore_UpdateUnknownIDIsNoOp(t *testing.T) {
	s := NewStore()
	// Must not panic on an id that was never Created.
	s.Update("does-not-exist", func(j *Job) { j.Hint = "boom" })
}

// --- (b) TryLock / Unlock single-flight ---------------------------------

func TestStore_TryLockSingleFlightPerNickname(t *testing.T) {
	s := NewStore()

	if !s.TryLock("router1") {
		t.Fatal("first TryLock(router1) should succeed")
	}
	if s.TryLock("router1") {
		t.Fatal("second TryLock(router1) should fail while still held")
	}
	if !s.TryLock("router2") {
		t.Fatal("TryLock(router2) should succeed independently of router1's lock")
	}

	s.Unlock("router1")
	if !s.TryLock("router1") {
		t.Fatal("TryLock(router1) should succeed again after Unlock")
	}

	s.Unlock("router1")
	s.Unlock("router2")
}

func TestStore_UnlockUnknownNicknameIsNoOp(t *testing.T) {
	s := NewStore()
	// Must not panic when unlocking a nickname that was never locked.
	s.Unlock("never-locked")
	if !s.TryLock("never-locked") {
		t.Fatal("TryLock should succeed after a no-op Unlock")
	}
}

// --- (c) Sweep with injected clock ---------------------------------------

func TestStore_SweepEvictsOldTerminalJobsButKeepsRunning(t *testing.T) {
	s := NewStore()
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return current }

	oldTerminal := s.Create(KindProvision, "old-terminal", []Step{{Name: "x", Status: StepPending}})
	stillRunning := s.Create(KindProvision, "still-running", []Step{{Name: "x", Status: StepPending}})

	// oldTerminal goes terminal now (t=0); its `ended` stamp is T0.
	s.Update(oldTerminal.ID, func(j *Job) { j.State = StateSuccess })

	// Advance the clock most of the way to jobTTL, then create+terminate a
	// second job so its `ended` stamp is recent relative to the sweep time.
	current = current.Add(jobTTL - time.Minute)
	freshTerminal := s.Create(KindProvision, "fresh-terminal", []Step{{Name: "x", Status: StepPending}})
	s.Update(freshTerminal.ID, func(j *Job) { j.State = StateFailed })

	// Advance past oldTerminal's TTL (t=0 + jobTTL+1min) but still within
	// freshTerminal's TTL (stamped at jobTTL-1min, so only 2min old here).
	current = current.Add(2 * time.Minute)

	s.Sweep()

	if _, ok := s.Get(oldTerminal.ID); ok {
		t.Error("terminal job older than jobTTL should have been evicted")
	}
	if _, ok := s.Get(stillRunning.ID); !ok {
		t.Error("running job must never be evicted by Sweep, regardless of age")
	}
	if _, ok := s.Get(freshTerminal.ID); !ok {
		t.Error("terminal job within jobTTL should still be present after Sweep")
	}
}

func TestStore_SweepWithNoTerminalJobsIsNoOp(t *testing.T) {
	s := NewStore()
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return current }

	job := s.Create(KindProvision, "router1", []Step{{Name: "x", Status: StepPending}})
	current = current.Add(24 * time.Hour) // far past jobTTL, but job never went terminal

	s.Sweep()

	if _, ok := s.Get(job.ID); !ok {
		t.Error("running job must survive Sweep even long after creation")
	}
}

// --- concurrency smoke (no -race available locally; see report) ---------

func TestStore_ConcurrentAccessDoesNotRace(t *testing.T) {
	s := NewStore()
	const n = 50

	ids := make([]string, n)
	for i := 0; i < n; i++ {
		job := s.Create(KindProvision, fmt.Sprintf("router-%d", i), []Step{{Name: "x", Status: StepPending}})
		ids[i] = job.ID
	}

	var wg sync.WaitGroup
	wg.Add(n * 4)
	for i := 0; i < n; i++ {
		id := ids[i]
		nick := fmt.Sprintf("router-%d", i)
		go func() {
			defer wg.Done()
			_, _ = s.Get(id)
		}()
		go func() {
			defer wg.Done()
			s.Update(id, func(j *Job) { j.Hint = "probed" })
		}()
		go func() {
			defer wg.Done()
			if s.TryLock(nick) {
				s.Unlock(nick)
			}
		}()
		go func() {
			defer wg.Done()
			s.Sweep()
		}()
	}
	wg.Wait()
}
