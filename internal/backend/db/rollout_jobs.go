package db

import (
	"database/sql"
	"errors"
	"time"
)

type RolloutJob struct {
	ID                   string     `json:"id"`
	TargetVersion        string     `json:"target_version"`
	State                string     `json:"state"`
	CreatedBy            int64      `json:"created_by,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	ObservationStartedAt *time.Time `json:"observation_started_at,omitempty"`
	PauseReason          string     `json:"pause_reason,omitempty"`
}

type RolloutTarget struct {
	JobID      string     `json:"-"`
	RouterID   int64      `json:"router_id"`
	Ordinal    int        `json:"ordinal"`
	Role       string     `json:"role"`
	Status     string     `json:"status"`
	CommandID  string     `json:"-"`
	QueuedAt   *time.Time `json:"queued_at,omitempty"`
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

type RolloutsRepo struct{ d *DB }

func (d *DB) Rollouts() *RolloutsRepo { return &RolloutsRepo{d: d} }

func (r *RolloutsRepo) Create(job RolloutJob, routerIDs []int64) error {
	tx, err := r.d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO rollout_jobs(id,target_version,state,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		job.ID, job.TargetVersion, job.State, job.CreatedBy, job.CreatedAt.UTC(), job.UpdatedAt.UTC()); err != nil {
		return err
	}
	for i, routerID := range routerIDs {
		role := "fleet"
		if i == 0 {
			role = "canary"
		}
		if _, err := tx.Exec(`INSERT INTO rollout_targets(job_id,router_id,ordinal,role) VALUES(?,?,?,?)`, job.ID, routerID, i, role); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *RolloutsRepo) Get(id string) (RolloutJob, []RolloutTarget, error) {
	var job RolloutJob
	var observed sql.NullTime
	if err := r.d.db.QueryRow(`SELECT id,target_version,state,created_by,created_at,updated_at,observation_started_at,pause_reason FROM rollout_jobs WHERE id=?`, id).
		Scan(&job.ID, &job.TargetVersion, &job.State, &job.CreatedBy, &job.CreatedAt, &job.UpdatedAt, &observed, &job.PauseReason); err != nil {
		return job, nil, err
	}
	job.CreatedAt, job.UpdatedAt = job.CreatedAt.UTC(), job.UpdatedAt.UTC()
	if observed.Valid {
		at := observed.Time.UTC()
		job.ObservationStartedAt = &at
	}
	rows, err := r.d.db.Query(`SELECT job_id,router_id,ordinal,role,status,command_id,queued_at,observed_at FROM rollout_targets WHERE job_id=? ORDER BY ordinal`, id)
	if err != nil {
		return job, nil, err
	}
	defer rows.Close()
	targets := []RolloutTarget{}
	for rows.Next() {
		var target RolloutTarget
		var queued, seen sql.NullTime
		if err := rows.Scan(&target.JobID, &target.RouterID, &target.Ordinal, &target.Role, &target.Status, &target.CommandID, &queued, &seen); err != nil {
			return job, nil, err
		}
		if queued.Valid {
			at := queued.Time.UTC()
			target.QueuedAt = &at
		}
		if seen.Valid {
			at := seen.Time.UTC()
			target.ObservedAt = &at
		}
		targets = append(targets, target)
	}
	return job, targets, rows.Err()
}

func (r *RolloutsRepo) Active() (RolloutJob, []RolloutTarget, error) {
	var id string
	if err := r.d.db.QueryRow(`SELECT id FROM rollout_jobs WHERE state NOT IN ('complete','failed','cancelled') ORDER BY created_at DESC LIMIT 1`).Scan(&id); err != nil {
		return RolloutJob{}, nil, err
	}
	return r.Get(id)
}

func (r *RolloutsRepo) Transition(id, from, to, reason string, at time.Time) error {
	res, err := r.d.db.Exec(`UPDATE rollout_jobs SET state=?,pause_reason=?,updated_at=?,observation_started_at=CASE WHEN ?='observing' THEN COALESCE(observation_started_at,?) ELSE observation_started_at END WHERE id=? AND state=?`,
		to, reason, at.UTC(), to, at.UTC(), id, from)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("rollout state changed")
	}
	return nil
}

func (r *RolloutsRepo) MarkTargetQueued(jobID string, routerID int64, commandID string, at time.Time) (bool, error) {
	res, err := r.d.db.Exec(`UPDATE rollout_targets SET status='queued',command_id=?,queued_at=? WHERE job_id=? AND router_id=? AND status='planned'`, commandID, at.UTC(), jobID, routerID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *RolloutsRepo) SetTargetCommand(jobID string, routerID int64, commandID string) error {
	_, err := r.d.db.Exec(`UPDATE rollout_targets SET command_id=? WHERE job_id=? AND router_id=? AND status='queued'`, commandID, jobID, routerID)
	return err
}

func (r *RolloutsRepo) MarkTargetObserved(jobID string, routerID int64, at time.Time) error {
	_, err := r.d.db.Exec(`UPDATE rollout_targets SET status='observed',observed_at=? WHERE job_id=? AND router_id=? AND status IN ('queued','observed')`, at.UTC(), jobID, routerID)
	return err
}

func (r *RolloutsRepo) Cancel(id string, at time.Time) error {
	_, err := r.d.db.Exec(`UPDATE rollout_jobs SET state='cancelled',updated_at=? WHERE id=? AND state NOT IN ('complete','failed','cancelled')`, at.UTC(), id)
	return err
}
