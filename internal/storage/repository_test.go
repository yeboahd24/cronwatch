package storage

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/schedule"
)

func testSpec(slug, expr string, grace time.Duration) JobSpec {
	return JobSpec{Slug: slug, Name: slug, Command: "true", Schedule: &expr, Grace: &grace}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInvalidStoredScheduleDoesNotBreakViews(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.UpsertJob(ctx, testSpec("feb30", "0 0 30 2 *", 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertJob(ctx, testSpec("hourly", "0 * * * *", 0)); err != nil {
		t.Fatal(err)
	}
	views, err := s.ListJobViews(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, v := range views {
		statuses[v.Slug] = v.Status
	}
	if statuses["feb30"] != "invalid_schedule" || statuses["hourly"] != "never_run" {
		t.Fatalf("statuses = %v", statuses)
	}
}

func TestUpsertKeepsUnsetFields(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if _, err := s.UpsertJob(ctx, testSpec("nightly", "0 2 * * *", 10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	job, err := s.UpsertJob(ctx, JobSpec{Slug: "nightly", Name: "Nightly", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule == nil || *job.Schedule != "0 2 * * *" || job.GraceSeconds != 600 {
		t.Fatalf("job = %+v", job)
	}
	empty := ""
	job, err = s.UpsertJob(ctx, JobSpec{Slug: "nightly", Name: "Nightly", Command: "true", Schedule: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule != nil {
		t.Fatalf("explicit empty schedule kept %q", *job.Schedule)
	}
	fresh, err := s.UpsertJob(ctx, JobSpec{Slug: "fresh", Name: "Fresh", Command: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.GraceSeconds != int64(DefaultGrace/time.Second) {
		t.Fatalf("default grace = %d", fresh.GraceSeconds)
	}
}

func TestReapAbandonedRuns(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("reap", "", 0))
	if err != nil {
		t.Fatal(err)
	}
	// A PID that has exited and been reaped, so it no longer exists.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	abandoned, err := s.CreateRun(ctx, job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "UPDATE runs SET pid = ? WHERE id = ?", dead.Process.Pid, abandoned.ID); err != nil {
		t.Fatal(err)
	}
	live, err := s.CreateRun(ctx, job.ID, time.Now()) // owned by this test process
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ReapAbandonedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d runs", n)
	}
	got, err := s.GetRun(ctx, abandoned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.EndedAt == nil || !strings.Contains(got.CombinedLog, "abandoned") {
		t.Fatalf("abandoned run = %+v", got)
	}
	if got, _ := s.GetRun(ctx, live.ID); got.Status != "running" {
		t.Fatalf("live run status = %s (pid %d)", got.Status, os.Getpid())
	}
}

func TestStaleRunningRunDoesNotHideMissed(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	job, err := s.UpsertJob(ctx, testSpec("hourly", "0 * * * *", time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	next, err := schedule.Next("0 * * * *", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Started for the previous occurrence and never finished.
	if _, err := s.CreateRun(ctx, job.ID, next.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DetectMissed(ctx, next.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	view, err := s.JobView(ctx, job, next.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "missed" {
		t.Fatalf("status = %s", view.Status)
	}
}

func TestOpenEnablesWAL(t *testing.T) {
	s := openTest(t)
	var mode string
	if err := s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %s", mode)
	}
}
