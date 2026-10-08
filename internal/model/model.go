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
	// MaxDurationSeconds is how long a heartbeat run may wait for its end
	// ping before it is recorded as timed out; 0 means no limit.
	MaxDurationSeconds int64
	// PausedAt is when the job was paused, until PausedUntil if that is set;
	// ArchivedAt is when it was archived. A paused or archived job is not
	// monitored: it is not checked for missed runs and raises no alerts.
	PausedAt, PausedUntil, ArchivedAt *time.Time
	// Tags group jobs for filtering; sorted, and nil for none.
	Tags []string
}

// Paused reports whether a pause is in effect at now.
func (j Job) Paused(now time.Time) bool {
	return j.PausedAt != nil && (j.PausedUntil == nil || now.Before(*j.PausedUntil))
}

// Monitored reports whether the job is checked for missed runs and raises
// alerts at now: it is neither archived nor paused.
func (j Job) Monitored(now time.Time) bool {
	return j.ArchivedAt == nil && !j.Paused(now)
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
	// FailureSignature groups failed runs that failed the same way; empty
	// for runs that did not fail.
	FailureSignature string
	// OutputAt is when the output saved while the run was running last
	// changed; nil if none was saved.
	OutputAt *time.Time
	// PID is the cronwatch process that ran the run; 0 for heartbeat runs.
	PID int64
}

// Usage is the resources a run's command used.
type Usage struct {
	MaxRSSKB  int64 // peak resident memory
	UserCPUMS int64
	SysCPUMS  int64
}

// RunStatuses are the statuses a finished or running run can have.
var RunStatuses = []string{"success", "failed", "timeout", "running", "cancelled", "skipped"}

// Failing reports whether a run status counts as a failure.
func Failing(status string) bool { return status == "failed" || status == "timeout" }

// Unarchived returns views without the archived jobs, which job lists leave out.
func Unarchived(views []JobView) []JobView {
	out := make([]JobView, 0, len(views))
	for _, v := range views {
		if v.ArchivedAt == nil {
			out = append(out, v)
		}
	}
	return out
}

type JobView struct {
	Job
	Status string
	// LastRun is a summary, without the run's output.
	LastRun        *Run
	NextExpectedAt *time.Time
	// MissedAt is the latest missed occurrence while Status is "missed".
	MissedAt *time.Time
}
