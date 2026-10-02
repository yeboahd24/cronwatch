package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/schedule"
)

func countMissed(t *testing.T, s *Store, jobID string) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow("SELECT count(*) FROM missed_occurrences WHERE job_id = ?", jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDetectMissedRecordsEveryOccurrence(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("hourly", "0 * * * *", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first, err := schedule.Next("0 * * * *", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Occurrences first .. first+4h; a late run covers the second one.
	if _, err := s.CreateRun(ctx, job.ID, first.Add(time.Hour+10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := first.Add(4*time.Hour + 2*time.Minute)
	n, err := s.DetectMissed(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || countMissed(t, s, job.ID) != 4 {
		t.Fatalf("recorded %d, stored %d; want 4", n, countMissed(t, s, job.ID))
	}
	// The watermark makes a second pass a no-op.
	if n, err := s.DetectMissed(ctx, now); err != nil || n != 0 {
		t.Fatalf("second pass recorded %d (%v)", n, err)
	}
	view, err := s.JobView(ctx, mustJob(t, s, "hourly"), now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "missed" {
		t.Fatalf("status = %s", view.Status)
	}
}

func TestDetectMissedRespectsGrace(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("hourly", "0 * * * *", 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first, err := schedule.Next("0 * * * *", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.DetectMissed(ctx, first.Add(9*time.Minute)); err != nil || n != 0 {
		t.Fatalf("within grace recorded %d (%v)", n, err)
	}
	if n, err := s.DetectMissed(ctx, first.Add(11*time.Minute)); err != nil || n != 1 {
		t.Fatalf("after grace recorded %d (%v)", n, err)
	}
	if countMissed(t, s, job.ID) != 1 {
		t.Fatal("missed occurrence not stored")
	}
}

func TestScheduleChangeResetsWatermark(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.UpsertJob(ctx, testSpec("job", "0 2 * * *", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetectMissed(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Pretend the job was last checked long ago, then change its schedule.
	if _, err := s.DB.ExecContext(ctx, "UPDATE jobs SET created_at = ?, missed_checked_until = ? WHERE slug = 'job'",
		timestamp(time.Now().Add(-48*time.Hour)), timestamp(time.Now().Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	job, err := s.UpsertJob(ctx, testSpec("job", "*/5 * * * *", 0))
	if err != nil {
		t.Fatal(err)
	}
	if job.MissedCheckedUntil == nil || time.Since(*job.MissedCheckedUntil) > time.Minute {
		t.Fatalf("watermark = %v", job.MissedCheckedUntil)
	}
	// Grace-only changes keep the watermark.
	before := *job.MissedCheckedUntil
	job, err = s.UpsertJob(ctx, JobSpec{Slug: "job", Name: "job", Command: "true", Grace: new(time.Duration(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if job.MissedCheckedUntil == nil || !job.MissedCheckedUntil.Equal(before) {
		t.Fatalf("watermark changed to %v", job.MissedCheckedUntil)
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("p", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-10 * 24 * time.Hour)
	zero := 0
	for i := range 5 {
		r, err := s.CreateRun(ctx, job.ID, base.Add(time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			continue // oldest stays running and must survive
		}
		if err := s.FinishRun(ctx, r.ID, r.StartedAt, 0, "success", &zero, "", "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Prune(ctx, 3, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 1 {
		t.Fatalf("keep pruned %d runs", result.Runs)
	}
	result, err = s.Prune(ctx, 0, base.Add(3*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunsForJob(ctx, job.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 1 || len(runs) != 3 || runs[2].Status != "running" {
		t.Fatalf("age prune %+v left %+v", result, runs)
	}
}

func mustJob(t *testing.T, s *Store, slug string) model.Job {
	t.Helper()
	j, err := s.GetJobBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
