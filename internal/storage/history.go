package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// RunFilter narrows a list of runs. Zero fields do not filter.
type RunFilter struct {
	JobID  string
	Status string
	// Since and Until bound when runs started: Since inclusive, Until not.
	Since, Until time.Time
}

// RunCursor marks where a page of runs ended. The next page starts with the
// run after it, newest first.
type RunCursor struct {
	StartedAt time.Time
	ID        string
}

// After returns the cursor for the run, to continue after it.
func After(r RunWithJob) *RunCursor {
	return &RunCursor{StartedAt: r.Run.StartedAt, ID: r.Run.ID}
}

// filterArgs converts f and before into query arguments; nil means unset.
func filterArgs(f RunFilter, before *RunCursor) (jobID, status, since, until, beforeTime any, beforeID sql.NullString) {
	if f.JobID != "" {
		jobID = f.JobID
	}
	if f.Status != "" {
		status = f.Status
	}
	if !f.Since.IsZero() {
		since = timestamp(f.Since)
	}
	if !f.Until.IsZero() {
		until = timestamp(f.Until)
	}
	if before != nil {
		beforeTime = timestamp(before.StartedAt)
		beforeID = sql.NullString{String: before.ID, Valid: true}
	}
	return
}

// ListRunSummaries returns up to limit runs matching f, newest first,
// starting after before if it is not nil. The runs are summaries: their
// output is not loaded.
func (s *Store) ListRunSummaries(ctx context.Context, f RunFilter, before *RunCursor, limit int) ([]RunWithJob, error) {
	p := db.ListRunSummariesPageParams{RowLimit: int64(limit)}
	p.JobID, p.Status, p.Since, p.Until, p.BeforeTime, p.BeforeID = filterArgs(f, before)
	rows, err := db.New(s.DB).ListRunSummariesPage(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]RunWithJob, 0, len(rows))
	for _, row := range rows {
		r, err := convertRunSummary(row.RunSummary)
		if err != nil {
			return nil, err
		}
		out = append(out, RunWithJob{Run: r, JobName: row.JobName})
	}
	return out, nil
}

// SearchRunLogs is ListRunSummaries, with output, for finished runs whose combined log
// contains query, ignoring ASCII case. An empty query matches every run.
func (s *Store) SearchRunLogs(ctx context.Context, query string, f RunFilter, before *RunCursor, limit int) ([]RunWithJob, error) {
	p := db.SearchRunLogsParams{Query: query, RowLimit: int64(limit)}
	p.JobID, p.Status, p.Since, p.Until, p.BeforeTime, p.BeforeID = filterArgs(f, before)
	rows, err := db.New(s.DB).SearchRunLogs(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]RunWithJob, 0, len(rows))
	for _, row := range rows {
		r, err := convertRun(row.Run)
		if err != nil {
			return nil, err
		}
		out = append(out, RunWithJob{Run: r, JobName: row.JobName})
	}
	return out, nil
}
