package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestPing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hook, events := hookLog(t)
	ping := func(args ...string) error {
		return Run(ctx, append([]string{"ping", "--data-dir", dir}, args...), &bytes.Buffer{}, &bytes.Buffer{})
	}
	runs := func() []string {
		s, err := storage.Open(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		job, err := s.GetJobBySlug(ctx, "etl")
		if err != nil {
			t.Fatal(err)
		}
		list, err := s.ListRunsForJob(ctx, job.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range slices.Backward(list) {

			out = append(out, r.Status+" "+strings.TrimSpace(r.Stdout+r.Stderr))
		}
		return out
	}

	// A plain ping creates the job and records a success.
	if err := ping("--nope", "etl"); err == nil {
		t.Fatal("unknown flag accepted")
	}
	t.Setenv(envOnFailure, hook)
	if err := ping("--name", "Nightly ETL", "--message", "loaded 120 rows", "etl"); err != nil {
		t.Fatal(err)
	}
	// --start then an end ping measures the run.
	if err := ping("--start", "etl"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := ping("--fail", "--exit-code", "4", "--message", "upstream API returned 503", "etl"); err != nil {
		t.Fatal(err)
	}
	// A start without an end is closed as failed by the next start.
	if err := ping("--start", "etl"); err != nil {
		t.Fatal(err)
	}
	if err := ping("--start", "etl"); err != nil {
		t.Fatal(err)
	}
	got := runs()
	want := []string{"success loaded 120 rows", "failed upstream API returned 503", "failed cronwatch: no end ping before the next start ping", "running "}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("runs = %q, want %q", got, want)
	}
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := s.GetJobBySlug(ctx, "etl")
	list, _ := s.ListRunsForJob(ctx, job.ID, 10)
	measured := list[2] // the failed run that followed a --start
	s.Close()
	if job.Name != "Nightly ETL" || measured.DurationMS == nil || *measured.DurationMS < 20 || *measured.ExitCode != 4 {
		t.Fatalf("job %q, measured run %+v", job.Name, measured)
	}
	// The failure alerted once; the second failure did not alert again.
	if e := events(); len(e) != 1 || !strings.HasPrefix(e[0], "failed etl failed 4 upstream API returned 503") {
		t.Fatalf("events = %q", e)
	}
	if err := ping("--start", "--fail", "etl"); err == nil {
		t.Fatal("--start --fail accepted")
	}
}

func TestHeartbeatRunTimesOut(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hook, events := hookLog(t)
	t.Setenv(envOnFailure, hook)
	ping := func(args ...string) error {
		return Run(ctx, append([]string{"ping", "--data-dir", dir}, args...), &bytes.Buffer{}, &bytes.Buffer{})
	}
	for _, bad := range []string{"-1s", "500ms"} {
		if err := ping("--max-duration", bad, "etl"); err == nil {
			t.Errorf("--max-duration %s accepted", bad)
		}
	}
	if err := ping("--start", "--max-duration", "1h", "etl"); err != nil {
		t.Fatal(err)
	}
	s := openStore(t, dir)
	job, err := s.GetJobBySlug(ctx, "etl")
	if err != nil || job.MaxDurationSeconds != 3600 {
		t.Fatalf("job = %+v, %v", job, err)
	}
	// Within the limit, maintenance leaves the open run alone.
	var errOut bytes.Buffer
	if err := maintain(ctx, s, &errOut); err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenHeartbeatRun(ctx, job.ID)
	if err != nil {
		t.Fatalf("open run: %v", err)
	}
	// Past it, maintenance times the run out and alerts without another ping.
	if _, err := s.DB.ExecContext(ctx, "UPDATE runs SET started_at = ? WHERE id = ?", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano), open.ID); err != nil {
		t.Fatal(err)
	}
	if err := maintain(ctx, s, &errOut); err != nil {
		t.Fatal(err)
	}
	run, err := s.GetRun(ctx, open.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "timeout" || run.Reason != "no end ping within 1h (--max-duration)" || run.DurationMS == nil || *run.DurationMS < (2*time.Hour).Milliseconds() {
		t.Fatalf("run = %+v", run)
	}
	if e := events(); len(e) != 1 || e[0] != "timeout etl timeout  cronwatch: no end ping within 1h (--max-duration)" {
		t.Fatalf("events = %q, stderr = %q", e, errOut.String())
	}
	if job, _ := s.JobView(ctx, job, time.Now()); job.Status != "timeout" {
		t.Fatalf("job status = %q", job.Status)
	}

	// A ping notices an overdue run itself, without waiting for maintenance:
	// the late end ping finds it timed out and records a run of its own. The
	// limit is kept by pings that do not pass it.
	if err := ping("--start", "etl"); err != nil {
		t.Fatal(err)
	}
	if job, _ = s.GetJobBySlug(ctx, "etl"); job.MaxDurationSeconds != 3600 {
		t.Fatalf("limit after a ping without it = %d", job.MaxDurationSeconds)
	}
	open, _ = s.OpenHeartbeatRun(ctx, job.ID)
	if _, err := s.DB.ExecContext(ctx, "UPDATE runs SET started_at = ? WHERE id = ?", time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339Nano), open.ID); err != nil {
		t.Fatal(err)
	}
	if err := ping("etl"); err != nil {
		t.Fatal(err)
	}
	if run, _ := s.GetRun(ctx, open.ID); run.Status != "timeout" {
		t.Fatalf("overdue run ended by a late ping = %+v", run)
	}

	// --max-duration 0 removes the limit.
	if err := ping("--max-duration", "0", "etl"); err != nil {
		t.Fatal(err)
	}
	if job, _ = s.GetJobBySlug(ctx, "etl"); job.MaxDurationSeconds != 0 {
		t.Fatalf("limit after --max-duration 0 = %d", job.MaxDurationSeconds)
	}
}
