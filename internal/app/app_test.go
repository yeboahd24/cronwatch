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
