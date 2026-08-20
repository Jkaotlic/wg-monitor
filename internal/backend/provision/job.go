// Package provision holds the async-job data model and in-memory store
// shared by the router-provisioning engine and its HTTP handlers.
//
// A Job tracks one long-running provision/register/repair run as an ordered
// checklist of Steps, so the dashboard can poll progress (GET
// /v1/dashboard/provision/{job_id}) while a goroutine drives the actual work
// on a server-owned context. Store is the concurrency-safe home for Jobs: it
// is written by that worker goroutine and read by HTTP handlers/pollers at
// the same time, so every map access is mutex-guarded and every value handed
// to a caller is a defensive copy — the only sanctioned way to mutate a
// stored Job afterwards is Store.Update.
package provision

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// JobKind identifies what kind of run a Job represents.
type JobKind string

const (
	KindProvision       JobKind = "provision"        // install now, new router
	KindRegister        JobKind = "register"         // mint token only
	KindRepairRepoint   JobKind = "repair_repoint"   // rewrite backend.url + restart
	KindRepairReinstall JobKind = "repair_reinstall" // full reinstall
)

// JobState is the overall run state of a Job.
type JobState string

const (
	StateRunning JobState = "running"
	StateSuccess JobState = "success"
	StateFailed  JobState = "failed"
)

// StepStatus is the state of a single Step within a Job's checklist.
type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepActive  StepStatus = "active"
	StepDone    StepStatus = "done"
	StepFailed  StepStatus = "failed"
)

// Step is one entry in a Job's checklist. Name is a stable id (matched
// against the per-kind step-catalog templates built by a later task);
// Detail is optional human-readable context, e.g. an error snippet recorded
// alongside StepFailed.
type Step struct {
	Name   string     `json:"name"`
	Status StepStatus `json:"status"`
	Detail string     `json:"detail,omitempty"`
}

// Job is one provision/register/repair run. Steps is seeded from a per-kind
// template at Create time so the client can render the full checklist as
// "pending" immediately, before the worker goroutine has made any progress.
//
// ended is unexported and deliberately excluded from JSON: it is bookkeeping
// for Sweep's TTL eviction, not API surface. It is set once, by Update, the
// first time a mutation moves State to a terminal value.
type Job struct {
	ID       string   `json:"id"`
	Nickname string   `json:"nickname"`
	Kind     JobKind  `json:"kind"`
	State    JobState `json:"state"`
	Steps    []Step   `json:"steps"`
	Version  string   `json:"version,omitempty"`
	Hint     string   `json:"hint,omitempty"`
	Tail     string   `json:"tail,omitempty"`
	ended    time.Time
}

// jobTTL is how long a terminal (success/failed) Job is retained after it
// ends, so a slow poller still observes the final state. Running jobs are
// never evicted, regardless of age.
const jobTTL = 30 * time.Minute

// Store is the concurrency-safe, in-memory home for Jobs plus the
// per-nickname single-flight locks that keep two concurrent provision/repair
// runs from racing the same agent. Zero value is not usable; use NewStore.
type Store struct {
	mu    sync.Mutex
	jobs  map[string]*Job
	locks map[string]bool
	now   func() time.Time // overridable for tests; nil -> time.Now
}

// NewStore returns an empty, ready-to-use Store.
func NewStore() *Store {
	return &Store{
		jobs:  make(map[string]*Job),
		locks: make(map[string]bool),
	}
}

func (s *Store) nowFn() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Create starts a new Job in StateRunning, deep-copying steps so the
// caller's slice — often a shared per-kind template — is never aliased by
// the store: two Jobs created from the same template slice never share a
// backing array, and mutating the caller's slice afterwards cannot reach
// into the store. The returned *Job is likewise a private copy rather than
// the pointer held internally by the store: the only sanctioned way to
// mutate a stored Job afterwards is Update.
func (s *Store) Create(kind JobKind, nick string, steps []Step) *Job {
	stored := &Job{
		ID:       newJobID(),
		Nickname: nick,
		Kind:     kind,
		State:    StateRunning,
		Steps:    copySteps(steps),
	}

	// Build the caller's return copy before stored is published into the
	// map below. Once s.jobs[id] is set it is reachable by a concurrent
	// Update, so reading stored's fields for the copy has to happen first —
	// otherwise this unlocked read could race an Update's locked write to
	// the very same *Job.
	out := *stored
	out.Steps = copySteps(stored.Steps)

	s.mu.Lock()
	s.jobs[stored.ID] = stored
	s.mu.Unlock()

	return &out
}

// Get returns a copy of the Job with the given id, safe to hold, mutate, or
// JSON-encode without racing the worker goroutine or corrupting stored
// state: Steps is deep-copied, not aliased.
func (s *Store) Get(id string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	out := *job
	out.Steps = copySteps(job.Steps)
	return out, true
}

// Update runs mutate against the stored Job under the store's lock — the
// only sanctioned way to change a Job after Create. If mutate moves State to
// a terminal value (StateSuccess/StateFailed), Update stamps ended the first
// time that happens; repeated Updates after the job is already terminal
// (e.g. to append a late Hint) do not push ended forward, so Sweep's TTL
// clock starts at actual completion. If id is unknown, Update is a no-op.
func (s *Store) Update(id string, mutate func(*Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	mutate(job)
	if (job.State == StateSuccess || job.State == StateFailed) && job.ended.IsZero() {
		job.ended = s.nowFn()
	}
}

// TryLock acquires the single-flight lock for nick, returning true if it was
// free (now held by the caller) or false if another run already holds it.
// Callers must pair a successful TryLock with a later Unlock.
// LatestFor возвращает задание этого агента: идущее, если оно есть, иначе
// самое свежее завершённое. Экран спрашивает «что сейчас с роутером», а не
// «что с заданием №такой-то»: операцию могли запустить с другого устройства,
// и её идентификатора у спрашивающего нет.
func (s *Store) LatestFor(nick string) (Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Job
	for _, job := range s.jobs {
		if job.Nickname != nick {
			continue
		}
		if best == nil {
			best = job
			continue
		}
		// Идущее перебивает завершённое; среди однородных -- то, что создано
		// позже (идентификаторы монотонны по времени создания).
		if job.State == StateRunning && best.State != StateRunning {
			best = job
			continue
		}
		if job.State == best.State && job.ID > best.ID {
			best = job
		}
	}
	if best == nil {
		return Job{}, false
	}
	out := *best
	out.Steps = copySteps(best.Steps)
	return out, true
}

func (s *Store) TryLock(nick string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[nick] {
		return false
	}
	s.locks[nick] = true
	return true
}

// Unlock releases the single-flight lock for nick. Unlocking a nick that
// isn't locked is a no-op.
func (s *Store) Unlock(nick string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.locks, nick)
}

// Sweep evicts terminal (success/failed) Jobs whose ended timestamp is older
// than jobTTL. Running Jobs are never evicted, regardless of age. Callers
// drive the schedule (mirrors cmd.Queue.Sweep / retention.Policy.Run).
func (s *Store) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.nowFn().Add(-jobTTL)
	for id, job := range s.jobs {
		if job.State == StateRunning {
			continue
		}
		if job.ended.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

// copySteps returns an independent copy of steps so no two Jobs — nor a Job
// and a caller-held slice — ever share a backing array.
func copySteps(steps []Step) []Step {
	if steps == nil {
		return nil
	}
	out := make([]Step, len(steps))
	copy(out, steps)
	return out
}

// newJobID returns a fresh 16-hex-char random id, 8 bytes of crypto/rand hex
// encoded — the same shape as internal/backend's newRequestID (reqid.go).
// That helper isn't imported here: a later task wires internal/backend's
// HTTP handlers to import this package, so the reverse import would cycle.
// Job ids are public identifiers used only to correlate polls (GET
// /v1/dashboard/provision/{job_id}), not bearer credentials, so this matches
// reqid's non-secret id shape rather than the longer tokens minted for
// enrollment/auth (e.g. wizard_handler.go's newAgentEnrollmentToken).
func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand read failure is effectively unreachable on supported
		// platforms; fall back to a fixed sentinel rather than panic so a
		// transient entropy hiccup can't take down the provisioning path.
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
