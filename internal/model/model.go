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
	// OnFailure and OnRecover are shell commands run when the job starts
	// failing or recovers; empty means none.
	OnFailure, OnRecover string
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
	// Reason explains a status the exit code alone does not, such as a
	// --fail-if-match failure, a timeout or a skipped run.
	Reason string
	// OverlappedRunID is the run of the same job that was still running
	// when this one started.
	OverlappedRunID string
	Usage           *Usage // nil when not measured
}

// Usage is the resources a run's command used.
type Usage struct {
	MaxRSSKB  int64 // peak resident memory
	UserCPUMS int64
	SysCPUMS  int64
}

// Failing reports whether a run status counts as a failure.
func Failing(status string) bool { return status == "failed" || status == "timeout" }

type JobView struct {
	Job
	Status         string
	LastRun        *Run
	NextExpectedAt *time.Time
	// MissedAt is the latest missed occurrence while Status is "missed".
	MissedAt *time.Time
}
