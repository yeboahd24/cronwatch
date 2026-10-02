package storage

import (
	"context"
	"testing"
	"time"
)

func TestMissedScheduleAndLateRun(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, testSpec("nightly", "0 2 * * *", 10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(time.Local)
	// Use the next occurrence after creation so a new job cannot be called missed before it existed.
	next, err := time.ParseInLocation("2006-01-02 15:04", now.Add(24*time.Hour).Format("2006-01-02")+" 02:00", time.Local)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	if _, err := s.DetectMissed(ctx, next.Add(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	view, err := s.JobView(ctx, job, next.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "missed" {
		t.Fatalf("status = %s", view.Status)
	}
	if _, err := s.DetectMissed(ctx, next.Add(12*time.Minute)); err != nil {
		t.Fatal(err)
	}
	view, err = s.JobView(ctx, job, next.Add(12*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "missed" {
		t.Fatalf("second status = %s", view.Status)
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM missed_occurrences WHERE job_id=?", job.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("missed records = %d", count)
	}
	run, err := s.CreateRun(ctx, job.ID, next.Add(13*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if err := s.FinishRun(ctx, run.ID, next.Add(13*time.Minute), 0, "success", &zero, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	view, err = s.JobView(ctx, job, next.Add(14*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "success" {
		t.Fatalf("late status = %s", view.Status)
	}
}

func TestRunsSortWithinSameSecond(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.UpsertJob(ctx, testSpec("order", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	a, err := s.CreateRun(ctx, job.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateRun(ctx, job.ID, first.Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunsForJob(ctx, job.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != b.ID || runs[1].ID != a.ID {
		t.Fatalf("run order = %+v", runs)
	}
}
