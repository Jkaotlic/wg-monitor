package db

import (
	"database/sql"
	"errors"
	"time"
)

var (
	ErrOperationNotFound        = errors.New("operation not found")
	ErrOperationAlreadyFinished = errors.New("operation already finished")
)

type Operation struct {
	Seq        int64      `json:"seq"`
	ID         string     `json:"operation_id"`
	RequestID  string     `json:"request_id,omitempty"`
	RouterID   int64      `json:"router_id"`
	ActorID    int64      `json:"actor_id,omitempty"`
	Action     string     `json:"action"`
	Source     string     `json:"source"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Outcome    string     `json:"outcome"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Summary    string     `json:"summary,omitempty"`
	CommandID  string     `json:"command_id,omitempty"`
}

type OperationFinish struct {
	FinishedAt time.Time
	Outcome    string
	ReasonCode string
	Summary    string
	CommandID  string
}

type OperationsRepo struct{ d *DB }

func (d *DB) Operations() *OperationsRepo { return &OperationsRepo{d: d} }

func (r *OperationsRepo) Begin(op Operation) error {
	_, err := r.d.db.Exec(`INSERT INTO operation_journal
		(operation_id, request_id, router_id, actor_id, action, source, started_at, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		op.ID, op.RequestID, op.RouterID, op.ActorID, op.Action, op.Source, op.StartedAt.UTC(), op.Summary)
	return err
}

func (r *OperationsRepo) Finish(id string, finish OperationFinish) error {
	res, err := r.d.db.Exec(`UPDATE operation_journal
		SET finished_at=?, outcome=?, reason_code=?, summary=CASE WHEN ?='' THEN summary ELSE ? END, command_id=?
		WHERE operation_id=? AND finished_at IS NULL`, finish.FinishedAt.UTC(), finish.Outcome,
		finish.ReasonCode, finish.Summary, finish.Summary, finish.CommandID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var finished sql.NullTime
	if err := r.d.db.QueryRow(`SELECT finished_at FROM operation_journal WHERE operation_id=?`, id).Scan(&finished); errors.Is(err, sql.ErrNoRows) {
		return ErrOperationNotFound
	} else if err != nil {
		return err
	}
	return ErrOperationAlreadyFinished
}

func (r *OperationsRepo) AttachCommand(id, commandID string) error {
	res, err := r.d.db.Exec(`UPDATE operation_journal SET command_id=?
		WHERE operation_id=? AND command_id='' AND finished_at IS NULL`, commandID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	return ErrOperationAlreadyFinished
}

func (r *OperationsRepo) FinishByCommand(commandID string, finish OperationFinish) error {
	var id string
	if err := r.d.db.QueryRow(`SELECT operation_id FROM operation_journal WHERE command_id=? ORDER BY seq DESC LIMIT 1`, commandID).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return ErrOperationNotFound
	} else if err != nil {
		return err
	}
	finish.CommandID = commandID
	return r.Finish(id, finish)
}

func (r *OperationsRepo) List(routerID int64, limit int, beforeSeq int64) ([]Operation, error) {
	if limit <= 0 {
		return []Operation{}, nil
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT seq, operation_id, request_id, router_id, actor_id, action, source,
		started_at, finished_at, outcome, reason_code, summary, command_id
		FROM operation_journal WHERE router_id=?`
	args := []any{routerID}
	if beforeSeq > 0 {
		query += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.d.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		var op Operation
		var finished sql.NullTime
		if err := rows.Scan(&op.Seq, &op.ID, &op.RequestID, &op.RouterID, &op.ActorID,
			&op.Action, &op.Source, &op.StartedAt, &finished, &op.Outcome, &op.ReasonCode,
			&op.Summary, &op.CommandID); err != nil {
			return nil, err
		}
		op.StartedAt = op.StartedAt.UTC()
		if finished.Valid {
			at := finished.Time.UTC()
			op.FinishedAt = &at
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
