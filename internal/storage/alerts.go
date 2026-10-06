package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// Alert statuses. A pending alert is waiting for its first attempt or a
// retry; undelivered means every attempt failed; cancelled means the job's
// hook was removed before the alert was delivered.
const (
	AlertPending     = "pending"
	AlertDelivered   = "delivered"
	AlertUndelivered = "undelivered"
	AlertCancelled   = "cancelled"
)

// Alert is one run of a job's --on-failure or --on-recover hook, kept until
// it is delivered or runs out of attempts.
type Alert struct {
	ID      string
	JobID   string
	RunID   string // "" for a missed run
	Event   string // failed, timeout, missed or recovered
	Hook    string // on_failure or on_recover
	Environ []string
	Created time.Time
	Status  string
	// Attempts counts attempts started, including one in progress.
	Attempts      int
	NextAttemptAt *time.Time // for a pending alert
	LastAttemptAt *time.Time
	LastError     string
	LastOutput    string
}

// PendingAlert is a pending alert with the job's name and current hook
// command, which a retry runs.
type PendingAlert struct {
	Alert
	JobName string
	Command string // "" if the job no longer has the hook
}

// NewAlert is an alert to queue.
type NewAlert struct {
	JobID   string
	RunID   string
	Event   string
	Hook    string
	Environ []string
	Created time.Time
}

// QueueAlert stores an alert, due at once.
func (s *Store) QueueAlert(ctx context.Context, a NewAlert) error {
	id, err := newID()
	if err != nil {
		return err
	}
	environ, err := json.Marshal(a.Environ)
	if err != nil {
		return err
	}
	return db.New(s.DB).InsertAlert(ctx, db.InsertAlertParams{ID: id, JobID: a.JobID,
		RunID: sql.NullString{String: a.RunID, Valid: a.RunID != ""}, Event: a.Event, Hook: a.Hook,
		Environ: string(environ), CreatedAt: timestamp(a.Created), NextAttemptAt: nullTimestamp(&a.Created)})
}

// PendingAlerts returns the pending alerts of the job with jobID, or of every
// job if jobID is "", oldest first within each job.
func (s *Store) PendingAlerts(ctx context.Context, jobID string) ([]PendingAlert, error) {
	var filter any
	if jobID != "" {
		filter = jobID
	}
	rows, err := db.New(s.DB).ListPendingAlerts(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]PendingAlert, 0, len(rows))
	for _, r := range rows {
		a, err := convertAlert(r.Alert)
		if err != nil {
			return nil, err
		}
		p := PendingAlert{Alert: a, JobName: r.JobName, Command: r.OnRecover.String}
		if a.Hook == "on_failure" {
			p.Command = r.OnFailure.String
		}
		out = append(out, p)
	}
	return out, nil
}

// ClaimAlert takes a due pending alert for one attempt, holding it until
// leaseUntil. It reports false if the alert is not due or another process
// took it first.
func (s *Store) ClaimAlert(ctx context.Context, id string, now, leaseUntil time.Time) (bool, error) {
	n, err := db.New(s.DB).ClaimAlert(ctx, db.ClaimAlertParams{ID: id, Now: nullTimestamp(&now), LeaseUntil: nullTimestamp(&leaseUntil)})
	return n == 1, err
}

// FinishAlertAttempt records how a claimed attempt went. A nil retryAt with
// status pending is invalid; retryAt is ignored for other statuses.
func (s *Store) FinishAlertAttempt(ctx context.Context, id, status string, retryAt *time.Time, errText, output string) error {
	if status != AlertPending {
		retryAt = nil
	}
	return db.New(s.DB).FinishAlertAttempt(ctx, db.FinishAlertAttemptParams{ID: id, Status: status,
		NextAttemptAt: nullTimestamp(retryAt), LastError: errText, LastOutput: output})
}

// CancelAlert stops retrying a pending alert, recording why.
func (s *Store) CancelAlert(ctx context.Context, id, reason string) error {
	return db.New(s.DB).CancelAlert(ctx, db.CancelAlertParams{ID: id, LastError: reason})
}

// JobAlerts returns up to limit of the job's alerts, newest first.
func (s *Store) JobAlerts(ctx context.Context, jobID string, limit int) ([]Alert, error) {
	rows, err := db.New(s.DB).ListJobAlerts(ctx, db.ListJobAlertsParams{JobID: jobID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return convertAlerts(rows)
}

// RunAlerts returns the alerts a run caused, oldest first.
func (s *Store) RunAlerts(ctx context.Context, runID string) ([]Alert, error) {
	rows, err := db.New(s.DB).ListRunAlerts(ctx, sql.NullString{String: runID, Valid: true})
	if err != nil {
		return nil, err
	}
	return convertAlerts(rows)
}

// UndeliveredAlerts counts, by job ID, alerts that have failed at least once
// and are not delivered: those still being retried and those given up on.
func (s *Store) UndeliveredAlerts(ctx context.Context) (map[string]int, error) {
	rows, err := db.New(s.DB).CountUndeliveredAlerts(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(rows))
	for _, r := range rows {
		out[r.JobID] = int(r.Alerts)
	}
	return out, nil
}

func convertAlerts(rows []db.Alert) ([]Alert, error) {
	out := make([]Alert, 0, len(rows))
	for _, r := range rows {
		a, err := convertAlert(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func convertAlert(r db.Alert) (Alert, error) {
	a := Alert{ID: r.ID, JobID: r.JobID, RunID: r.RunID.String, Event: r.Event, Hook: r.Hook, Status: r.Status,
		Attempts: int(r.Attempts), LastError: r.LastError, LastOutput: r.LastOutput}
	if err := json.Unmarshal([]byte(r.Environ), &a.Environ); err != nil {
		return a, err
	}
	var err error
	if a.Created, err = parseTime(r.CreatedAt); err != nil {
		return a, err
	}
	if a.NextAttemptAt, err = parseNullTime(r.NextAttemptAt); err != nil {
		return a, err
	}
	a.LastAttemptAt, err = parseNullTime(r.LastAttemptAt)
	return a, err
}

func nullTimestamp(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: timestamp(*t), Valid: true}
}

func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid {
		return nil, nil
	}
	t, err := parseTime(v.String)
	return &t, err
}
