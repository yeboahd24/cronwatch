package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/schedule"
)

// maxOccurrencesPerPass bounds one detection pass for a job, so a frequent
// schedule after a long outage is caught up over several passes.
const maxOccurrencesPerPass = 10000

// DetectMissed records every scheduled occurrence whose grace period has
// passed without a run starting between it and the next occurrence. Each job
// keeps a watermark, so a pass only examines occurrences since the last one.
func (s *Store) DetectMissed(ctx context.Context, now time.Time) (int, error) {
	missed, err := s.DetectMissedJobs(ctx, now)
	n := 0
	for _, m := range missed {
		n += len(m.ExpectedAt)
	}
	return n, err
}

// NewlyMissed is a job's occurrences that one detection pass recorded.
type NewlyMissed struct {
	Job        model.Job
	ExpectedAt []time.Time // oldest first
}

// DetectMissedJobs is DetectMissed returning the occurrences it recorded. An
// occurrence another process recorded first is not returned, so each one is
// reported once.
func (s *Store) DetectMissedJobs(ctx context.Context, now time.Time) ([]NewlyMissed, error) {
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		return nil, err
	}
	var out []NewlyMissed
	for _, j := range jobs {
		times, err := s.detectMissedForJob(ctx, j, now)
		if err != nil {
			return out, err
		}
		if len(times) > 0 {
			out = append(out, NewlyMissed{Job: j, ExpectedAt: times})
		}
	}
	return out, nil
}

func (s *Store) detectMissedForJob(ctx context.Context, j model.Job, now time.Time) ([]time.Time, error) {
	if j.Schedule == nil || !j.Monitored(now) {
		return nil, nil
	}
	sched, err := schedule.Parse(*j.Schedule)
	if err != nil {
		return nil, nil // reported as invalid_schedule by JobView
	}
	from := j.CreatedAt
	if j.MissedCheckedUntil != nil && j.MissedCheckedUntil.After(from) {
		from = *j.MissedCheckedUntil
	}
	// Occurrences during a pause that has run out were not expected.
	if j.PausedUntil != nil && j.PausedUntil.After(from) {
		from = *j.PausedUntil
	}
	deadline := now.Add(-time.Duration(j.GraceSeconds) * time.Second)
	occurrence := sched.Next(from.In(time.Local))
	if occurrence.IsZero() || occurrence.After(deadline) {
		return nil, nil
	}

	startValues, err := db.New(s.DB).ListRunStartsSince(ctx, db.ListRunStartsSinceParams{JobID: j.ID, StartedAt: timestamp(occurrence)})
	if err != nil {
		return nil, err
	}
	starts := make([]time.Time, len(startValues))
	for i, v := range startValues {
		if starts[i], err = parseTime(v); err != nil {
			return nil, err
		}
	}

	// Read before the transaction so it only writes: a deferred read-then-write
	// transaction can fail with SQLITE_BUSY_SNAPSHOT under concurrent runs.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := db.New(tx)

	detected := timestamp(now)
	var recorded []time.Time
	checked := from
	for i := 0; i < maxOccurrencesPerPass && !occurrence.IsZero() && !occurrence.After(deadline); i++ {
		next := sched.Next(occurrence)
		// Skip runs that started before this occurrence.
		for len(starts) > 0 && starts[0].Before(occurrence) {
			starts = starts[1:]
		}
		// A run counts for this occurrence if it started before the next one.
		if len(starts) == 0 || (!next.IsZero() && !starts[0].Before(next)) {
			id, err := newID()
			if err != nil {
				return nil, err
			}
			n, err := q.RecordMissedOccurrence(ctx, db.RecordMissedOccurrenceParams{ID: id, JobID: j.ID, ExpectedAt: timestamp(occurrence), DetectedAt: detected})
			if err != nil {
				return nil, err
			}
			if n == 1 {
				recorded = append(recorded, occurrence)
			}
		}
		checked, occurrence = occurrence, next
	}
	err = q.SetMissedCheckedUntil(ctx, db.SetMissedCheckedUntilParams{MissedCheckedUntil: sql.NullString{String: timestamp(checked), Valid: true}, ID: j.ID})
	if err != nil {
		return nil, err
	}
	return recorded, tx.Commit()
}
