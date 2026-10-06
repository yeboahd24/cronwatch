package storage

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/runner"
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

func TestAbandonedRuns(t *testing.T) {
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
	if err := s.SaveRunOutput(ctx, abandoned.ID, runner.Output{Stdout: "so far\n", Combined: "so far\n"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Only the run whose owner has gone is abandoned; its output is loaded.
	runs, err := s.AbandonedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != abandoned.ID || runs[0].PID != int64(dead.Process.Pid) || runs[0].Stdout != "so far\n" {
		t.Fatalf("abandoned = %+v; the live run %s belongs to pid %d", runs, live.ID, os.Getpid())
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

func TestOpenRelativeDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open(context.Background(), "data")
	if err != nil {
		t.Fatalf("relative data dir: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat("data/cronwatch.db"); err != nil {
		t.Fatalf("database not created under the relative directory: %v", err)
	}
}
