package rollout

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

var errDispatchUncertain = errors.New("rollout target dispatch is uncertain")

const ReportFreshness = 5 * time.Minute

type Candidate struct {
	RouterID      int64
	Fresh         bool
	HardIncidents int
}

func SelectCanary(candidates []Candidate) int64 {
	ordered := append([]Candidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Fresh != b.Fresh {
			return a.Fresh
		}
		if (a.HardIncidents == 0) != (b.HardIncidents == 0) {
			return a.HardIncidents == 0
		}
		return a.RouterID < b.RouterID
	})
	if len(ordered) == 0 || !ordered[0].Fresh {
		return 0
	}
	return ordered[0].RouterID
}

type Probe struct {
	Fresh            bool
	Version          string
	NewHardIncidents int
	ReportGap        bool
	LastSeenAt       time.Time
	PendingVersion   string
}

type Engine struct {
	DB                *db.DB
	ObservationWindow time.Duration
	Now               func() time.Time
	Deploy            func(routerID int64, target string) (commandID string, err error)
	Probe             func(routerID int64) Probe
}

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}
func (e Engine) window() time.Duration {
	if e.ObservationWindow > 0 {
		return e.ObservationWindow
	}
	return 10 * time.Minute
}

func rolloutID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "rollout-0000000000000000"
	}
	return "rollout-" + hex.EncodeToString(b[:])
}

func (e Engine) Create(target string, actor int64, routerIDs []int64, replacePausedID string) (db.RolloutJob, error) {
	if e.DB == nil || len(routerIDs) == 0 {
		return db.RolloutJob{}, errors.New("rollout has no targets")
	}
	now := e.now()
	job := db.RolloutJob{ID: rolloutID(), TargetVersion: target, State: "planned", CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
	if err := e.DB.Rollouts().Create(job, routerIDs, replacePausedID); err != nil {
		return db.RolloutJob{}, err
	}
	return job, nil
}

func (e Engine) Tick(id string) (db.RolloutJob, error) {
	job, targets, err := e.DB.Rollouts().Get(id)
	if err != nil {
		return job, err
	}
	now := e.now()
	switch job.State {
	case "planned":
		if len(targets) == 0 {
			return job, errors.New("rollout has no targets")
		}
		if err := e.queue(job, targets[0], now); err != nil {
			reason := "deploy_failed"
			if errors.Is(err, errDispatchUncertain) {
				reason = "dispatch_uncertain"
			}
			_ = e.DB.Rollouts().Transition(id, "planned", "paused", reason, now)
			break
		}
		if err := e.DB.Rollouts().Transition(id, "planned", "canary_pending", "", now); err != nil {
			return job, err
		}
	case "canary_pending":
		probe := e.probe(targets[0].RouterID)
		if reason := badGate(probe); reason != "" {
			if err := e.DB.Rollouts().Transition(id, "canary_pending", "paused", reason, now); err != nil {
				return job, err
			}
			break
		}
		if probe.Fresh && probe.Version == job.TargetVersion {
			_ = e.DB.Rollouts().MarkTargetObserved(id, targets[0].RouterID, now)
			if err := e.DB.Rollouts().Transition(id, "canary_pending", "observing", "", now); err != nil {
				return job, err
			}
		}
	case "observing":
		probe := e.probe(targets[0].RouterID)
		if reason := badGate(probe); reason != "" {
			if err := e.DB.Rollouts().Transition(id, "observing", "paused", reason, now); err != nil {
				return job, err
			}
			break
		}
		if job.ObservationStartedAt != nil && now.Sub(*job.ObservationStartedAt) >= e.window() {
			for _, target := range targets[1:] {
				if target.Status == "planned" {
					if err := e.queue(job, target, now); err != nil {
						reason := "deploy_failed"
						if errors.Is(err, errDispatchUncertain) {
							reason = "dispatch_uncertain"
						}
						_ = e.DB.Rollouts().Transition(id, "observing", "paused", reason, now)
						return e.current(id)
					}
				}
			}
			if err := e.DB.Rollouts().Transition(id, "observing", "rolling", "", now); err != nil {
				return job, err
			}
		}
	case "rolling":
		all := true
		for _, target := range targets {
			if target.Status == "observed" {
				continue
			}
			probe := e.probe(target.RouterID)
			reason := badGate(probe)
			// The pending update is durable and will run on contact. An
			// already-offline fleet router has no pre-update reports to lose.
			if reason == "report_gap" && target.Role == "fleet" && target.Status == "queued" && target.QueuedAt != nil &&
				probe.PendingVersion == job.TargetVersion &&
				probe.LastSeenAt.Before(target.QueuedAt.Add(-ReportFreshness)) {
				continue
			}
			if reason != "" {
				if err := e.DB.Rollouts().Transition(id, "rolling", "paused", reason, now); err != nil {
					return job, err
				}
				return e.current(id)
			}
			if probe.Fresh && probe.Version == job.TargetVersion {
				_ = e.DB.Rollouts().MarkTargetObserved(id, target.RouterID, now)
			} else {
				all = false
			}
		}
		if all {
			if err := e.DB.Rollouts().Transition(id, "rolling", "complete", "", now); err != nil {
				return job, err
			}
		}
	}
	return e.current(id)
}

func (e Engine) current(id string) (db.RolloutJob, error) {
	job, _, err := e.DB.Rollouts().Get(id)
	return job, err
}
func (e Engine) probe(id int64) Probe {
	if e.Probe == nil {
		return Probe{}
	}
	return e.Probe(id)
}
func badGate(p Probe) string {
	if p.NewHardIncidents > 0 {
		return "new_hard_incident"
	}
	if p.ReportGap {
		return "report_gap"
	}
	return ""
}

func (e Engine) queue(job db.RolloutJob, target db.RolloutTarget, now time.Time) error {
	// A non-planned target without a durable command ID is the crash window
	// between reserving it and recording the enqueue result. We cannot know
	// whether the external side effect happened, so fail closed for review.
	if target.Status != "planned" {
		if target.CommandID == "" || target.CommandID == "dispatching" {
			return errDispatchUncertain
		}
		return nil
	}
	reserved, err := e.DB.Rollouts().MarkTargetQueued(job.ID, target.RouterID, "dispatching", now)
	if err != nil {
		return err
	}
	if !reserved {
		return errDispatchUncertain
	}
	if e.Deploy == nil {
		return errors.New("deploy unavailable")
	}
	cmd, err := e.Deploy(target.RouterID, job.TargetVersion)
	if err != nil {
		return err
	}
	return e.DB.Rollouts().SetTargetCommand(job.ID, target.RouterID, cmd)
}

func (e Engine) Cancel(id string) error { return e.DB.Rollouts().Cancel(id, e.now()) }
