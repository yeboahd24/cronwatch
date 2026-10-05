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
