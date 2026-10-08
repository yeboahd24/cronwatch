package storage

import (
	"context"
	"testing"
	"time"
)

// An "@every" job is missed when a period and its grace pass without a run
// starting, counted from its last start.
func TestEveryJobMissedRuns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("sync", "@every 1h", 5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t0 := job.CreatedAt
	detect := func(at time.Duration) int {
		t.Helper()
		n, err := s.DetectMissed(ctx, t0.Add(at))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	view := func(at time.Duration) (string, time.Time) {
		t.Helper()
		v, err := s.JobView(ctx, mustJob(t, s, "sync"), t0.Add(at))
		if err != nil {
			t.Fatal(err)
		}
		if v.NextExpectedAt == nil {
			t.Fatalf("at +%s: no next expected run", at)
		}
		return v.Status, *v.NextExpectedAt
	}

	if _, next := view(10 * time.Minute); !next.Equal(t0.Add(time.Hour)) {
		t.Errorf("a new job is first expected at %v, want an hour after it was created", next.Sub(t0))
	}
	if n := detect(64 * time.Minute); n != 0 {
		t.Fatalf("missed %d within the grace", n)
	}
	if n := detect(66 * time.Minute); n != 1 {
		t.Fatalf("missed %d after the grace, want 1", n)
	}
	if status, next := view(66 * time.Minute); status != "missed" || !next.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("after a miss: %s, next at +%s", status, next.Sub(t0))
	}

	// A run restarts the count from its start.
	if _, err := s.CreateRun(ctx, job.ID, t0.Add(70*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if status, next := view(71 * time.Minute); status == "missed" || !next.Equal(t0.Add(130*time.Minute)) {
		t.Errorf("after a run: %s, next at +%s, want +2h10m", status, next.Sub(t0))
	}
	if n := detect(130 * time.Minute); n != 0 {
		t.Fatalf("missed %d an hour after the run, within the grace", n)
	}
	// Silence after that is missed once a period.
	if n := detect(4 * time.Hour); n != 2 {
		t.Fatalf("missed %d by +4h, want 2 (+2h10m and +3h10m)", n)
	}
	if n := detect(4 * time.Hour); n != 0 {
		t.Fatalf("a second pass missed %d", n)
	}
}

// Changing a cron job to "@every" counts from the change, not from before.
func TestEveryAfterScheduleChange(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.UpsertJob(ctx, testSpec("sync", "0 0 1 1 *", time.Minute)); err != nil {
		t.Fatal(err)
	}
	job, err := s.UpsertJob(ctx, testSpec("sync", "@every 2h", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if job.MissedCheckedUntil == nil {
		t.Fatal("the schedule change left no watermark")
	}
	n, err := s.DetectMissed(ctx, job.MissedCheckedUntil.Add(2*time.Hour-time.Second))
	if err != nil || n != 0 {
		t.Fatalf("missed %d (%v) before a period passed since the change", n, err)
	}
}
