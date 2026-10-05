package storage

import (
	"context"
	"testing"
	"time"
)

func TestJobTrend(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("report", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	day := 24 * time.Hour
	one := 1
	add := func(started time.Time, d time.Duration, status string) string {
		run, err := s.CreateRun(ctx, job.ID, started)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, Completion{Ended: started.Add(d), Duration: d, Status: status, ExitCode: &one}); err != nil {
			t.Fatal(err)
		}
		return run.ID
	}
	// A month at about 2 minutes, then a week at about 3 minutes.
	for i := 37; i >= 8; i-- {
		add(now.Add(-time.Duration(i)*day), 120*time.Second, "success")
	}
	for i := 7; i >= 1; i-- {
		add(now.Add(-time.Duration(i)*day), 180*time.Second, "success")
	}
	failed := add(now.Add(-12*time.Hour), time.Second, "failed")
	slow := add(now.Add(-time.Hour), 10*time.Minute, "success")

	trend, err := s.JobTrend(ctx, job.ID, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend.Runs) != 10 || trend.Runs[9].ID != slow || trend.Runs[8].ID != failed {
		t.Fatalf("runs = %+v", trend.Runs)
	}
	// Its baseline is the 20 successes before it: 7 at 3m and 13 at 2m.
	if sl, ok := trend.Slow[slow]; !ok || sl.Usual != 120*time.Second || sl.Factor != 5 {
		t.Fatalf("slow run not flagged: %+v", trend.Slow)
	}
	// The first 3-minute run is 1.5x its predecessors: slower, but not slow.
	if len(trend.Slow) != 1 {
		t.Fatalf("slow = %+v", trend.Slow)
	}
	if trend.Usual == 0 {
		t.Fatal("no usual duration")
	}
	if trend.Drift == nil || trend.Drift.Prior != 120*time.Second || trend.Drift.Percent < 50 {
		t.Fatalf("drift = %+v", trend.Drift)
	}
}
