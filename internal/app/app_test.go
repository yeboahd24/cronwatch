package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestRunRecordsResultsAndExitCode(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name, script, status string
		code                 int
		output               string
	}{
		{"good", "echo hello", "success", 0, "hello"},
		{"bad", "echo boom >&2; exit 7", "failed", 7, "boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := Run(context.Background(), []string{"run", "--name", tc.name, "--data-dir", dir, "--", "sh", "-c", tc.script}, &out, &errOut)
			if tc.code == 0 && err != nil {
				t.Fatal(err)
			}
			if tc.code != 0 {
				var exit *ExitError
				if !errors.As(err, &exit) || exit.Code != tc.code {
					t.Fatalf("exit error = %v", err)
				}
			}
			if !strings.Contains(out.String()+errOut.String(), tc.output) {
				t.Fatalf("echo missing: %q %q", out.String(), errOut.String())
			}
			s, e := storage.Open(context.Background(), dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			job, e := s.GetJobBySlug(context.Background(), tc.name)
			if e != nil {
				t.Fatal(e)
			}
			runs, e := s.ListRunsForJob(context.Background(), job.ID, 10)
			if e != nil {
				t.Fatal(e)
			}
			if len(runs) != 1 || runs[0].Status != tc.status || runs[0].ExitCode == nil || *runs[0].ExitCode != tc.code {
				t.Fatalf("runs = %+v", runs)
			}
			if !strings.Contains(runs[0].CombinedLog, tc.output) {
				t.Fatalf("log = %q", runs[0].CombinedLog)
			}
		})
	}
}

func TestRunRejectsInvalidSchedule(t *testing.T) {
	err := Run(context.Background(), []string{"run", "--name", "bad", "--schedule", "every day", "--data-dir", t.TempDir(), "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "invalid cron") {
		t.Fatalf("error = %v", err)
	}
}

func TestConcurrentRuns(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errorsCh := make(chan error, 10)
	for range 10 {
		wg.Go(func() {
			errorsCh <- Run(context.Background(), []string{"run", "--name", "shared", "--data-dir", dir, "--", "sh", "-c", "echo ok"}, &bytes.Buffer{}, &bytes.Buffer{})
		})
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := storage.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.GetJobBySlug(context.Background(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunsForJob(context.Background(), job.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 10 {
		t.Fatalf("got %d runs", len(runs))
	}
}

func TestRunRejectsImpossibleScheduleAndHugeLogs(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--name", "x", "--schedule", "0 0 30 2 *", "--data-dir", t.TempDir(), "--", "true"},
		{"run", "--name", "x", "--max-log-bytes", "1073741824", "--data-dir", t.TempDir(), "--", "true"},
	} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("%v: expected error", args)
		}
	}
}

func TestRunWithoutScheduleKeepsStoredSchedule(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	for _, args := range [][]string{
		{"run", "--name", "nightly", "--schedule", "0 2 * * *", "--grace", "10m", "--data-dir", dir, "--", "true"},
		{"run", "--name", "nightly", "--data-dir", dir, "--", "true"},
	} {
		if err := Run(ctx, args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.GetJobBySlug(ctx, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if job.Schedule == nil || *job.Schedule != "0 2 * * *" || job.GraceSeconds != 600 {
		t.Fatalf("job = %+v", job)
	}
}

func TestSlugCollisionWarns(t *testing.T) {
	dir := t.TempDir()
	var first, second bytes.Buffer
	if err := Run(context.Background(), []string{"run", "--name", "Back up", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &first); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"run", "--name", "back-up", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &second); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.String(), "warning") || !strings.Contains(second.String(), `slug "back-up" belongs to job "Back up"`) {
		t.Fatalf("stderr = %q / %q", first.String(), second.String())
	}
}

func TestPruneCommand(t *testing.T) {
	dir := t.TempDir()
	for range 3 {
		if err := Run(context.Background(), []string{"run", "--name", "p", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Run(context.Background(), []string{"prune", "--data-dir", dir}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("prune without limits should fail")
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"prune", "--keep", "1", "--data-dir", dir}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Deleted 2 runs") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestHelpForEveryCommand(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--help"}, {"run", "-h", "--", "true"}, {"serve", "--help"}, {"jobs", "-h"},
		{"runs", "--help"}, {"prune", "--help"}, {"sync", "--help"}, {"help", "run"},
		{"envdiff", "--help"}, {"try", "-h"}, {"digest", "--help"}, {"crontab-history", "-h"},
	} {
		var out bytes.Buffer
		if err := Run(context.Background(), args, &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.HasPrefix(out.String(), "Usage: cronwatch "+map[bool]string{true: args[1], false: args[0]}[args[0] == "help"]) {
			t.Fatalf("%v: output = %q", args, out.String())
		}
	}
	if err := Run(context.Background(), []string{"jobs", "--nope"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "-nope") {
		t.Fatalf("bad flag error = %v", err)
	}
}
