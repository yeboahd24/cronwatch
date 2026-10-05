package model

import "time"

type Job struct {
	ID           string
	Slug         string
	Name         string
	Command      string
	Schedule     *string
	GraceSeconds int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// MissedCheckedUntil is the missed-run detection watermark; nil means
	// detection starts from CreatedAt.
	MissedCheckedUntil *time.Time
}

type Run struct {
	ID          string
	JobID       string
	StartedAt   time.Time
	EndedAt     *time.Time
	DurationMS  *int64
	Status      string
	ExitCode    *int
	Stdout      string
	Stderr      string
	CombinedLog string
	Truncated   bool
	CreatedAt   time.Time
	// EnvHash identifies the run's recorded environment; empty for runs
	// recorded before environments were captured.
	EnvHash string
}

type JobView struct {
	Job
	Status         string
	LastRun        *Run
	NextExpectedAt *time.Time
	// MissedAt is the latest missed occurrence while Status is "missed".
	MissedAt *time.Time
}
