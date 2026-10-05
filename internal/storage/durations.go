package storage

import (
	"context"
	"slices"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/durations"
)

func ms(n int64) time.Duration { return time.Duration(n) * time.Millisecond }

// SuccessDurationsBefore returns the durations of up to limit of the job's
// successful runs that started before t, newest first.
func (s *Store) SuccessDurationsBefore(ctx context.Context, jobID string, t time.Time, limit int) ([]time.Duration, error) {
	rows, err := db.New(s.DB).SuccessDurationsBefore(ctx, db.SuccessDurationsBeforeParams{JobID: jobID, StartedAt: timestamp(t), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]time.Duration, 0, len(rows))
	for _, d := range rows {
		out = append(out, ms(d.Int64))
	}
	return out, nil
}

// RunDuration is a finished run without its output.
type RunDuration struct {
	ID        string
	StartedAt time.Time
	Duration  time.Duration
	Status    string
}

// Trend is a job's recent durations and what they say.
type Trend struct {
	Runs  []RunDuration                 // newest finished runs, oldest first
	Usual time.Duration                 // median of the last durations.Baseline successful runs; 0 if too few
	Slow  map[string]durations.Slowness // runs in Runs that were unusually slow, by ID
	Drift *durations.Drift              // set when the job is getting slower
}

// JobTrend returns the job's last n finished runs with slow runs marked, its
// usual duration and whether it is getting slower, as of now.
func (s *Store) JobTrend(ctx context.Context, jobID string, n int, now time.Time) (Trend, error) {
	q := db.New(s.DB)
	t := Trend{Slow: map[string]durations.Slowness{}}
	rows, err := q.ListRunDurations(ctx, db.ListRunDurationsParams{JobID: jobID, Limit: int64(n)})
	if err != nil {
		return t, err
	}
	for _, row := range slices.Backward(rows) {
		started, err := parseTime(row.StartedAt)
		if err != nil {
			return t, err
		}
		t.Runs = append(t.Runs, RunDuration{ID: row.ID, StartedAt: started, Duration: ms(row.DurationMs.Int64), Status: row.Status})
	}
	// Each successful run is judged against the successes before it. One read
	// of n+Baseline successes covers every run shown.
	successes, err := s.SuccessDurationsBefore(ctx, jobID, now.Add(time.Second), n+durations.Baseline)
	if err != nil {
		return t, err
	}
	if len(successes) >= durations.MinSamples {
		t.Usual = durations.Median(successes[:min(len(successes), durations.Baseline)])
	}
	seen := 0 // successes in Runs newer than the current one
	for _, r := range slices.Backward(t.Runs) {

		if r.Status != "success" {
			continue
		}
		seen++
		if seen >= len(successes) {
			break
		}
		earlier := successes[seen:min(len(successes), seen+durations.Baseline)]
		if sl, ok := durations.Slow(r.Duration, earlier); ok {
			t.Slow[r.ID] = sl
		}
	}
	since := now.Add(-durations.RecentWindow - durations.PriorWindow)
	points, err := q.SuccessDurationsSince(ctx, db.SuccessDurationsSinceParams{JobID: jobID, StartedAt: timestamp(since)})
	if err != nil {
		return t, err
	}
	var ps []durations.Point
	for _, p := range points {
		started, err := parseTime(p.StartedAt)
		if err != nil {
			return t, err
		}
		ps = append(ps, durations.Point{Start: started, Duration: ms(p.DurationMs.Int64)})
	}
	if d, ok := durations.Drifting(ps, now); ok {
		t.Drift = &d
	}
	return t, nil
}
