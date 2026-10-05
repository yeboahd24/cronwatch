package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestCheck(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	check := func(args ...string) (int, string) {
		var out bytes.Buffer
		err := Run(ctx, append([]string{"check", "--data-dir", dir}, args...), &out, &bytes.Buffer{})
		if exit, ok := errors.AsType[*ExitError](err); ok {
			return exit.Code, out.String()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0, out.String()
	}
	if code, out := check(); code != 0 || out != "CRONWATCH OK - 0 jobs ok | jobs=0 critical=0 warning=0 ok=0\n" {
		t.Fatalf("empty: %d %q", code, out)
	}
	_ = Run(ctx, []string{"run", "--name", "Good", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{})
	if code, out := check(); code != 0 || !strings.HasPrefix(out, "CRONWATCH OK - 1 job ok | jobs=1 critical=0") {
		t.Fatalf("one good job: %d %q", code, out)
	}
	_ = Run(ctx, []string{"run", "--name", "Bad", "--data-dir", dir, "--", "sh", "-c", "echo 'disk full' >&2; exit 1"}, &bytes.Buffer{}, &bytes.Buffer{})
	code, out := check()
	if code != 2 || !strings.HasPrefix(out, "CRONWATCH CRITICAL - Bad failed | jobs=2 critical=1 warning=0 ok=1\nCRITICAL: Bad: failed ") ||
		!strings.Contains(out, ": disk full\n") {
		t.Fatalf("one failing job: %d %q", code, out)
	}
	// Checking only the good job ignores the bad one.
	if code, out := check("good"); code != 0 || !strings.Contains(out, "jobs=1 critical=0") {
		t.Fatalf("check good: %d %q", code, out)
	}
	if code, out := check("nope"); code != 3 || out != "CRONWATCH UNKNOWN - no job with slug \"nope\"\n" {
		t.Fatalf("unknown slug: %d %q", code, out)
	}
}

func TestCheckWarnsAboutSlowerJobsInWholeSeconds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "export", Name: "Export", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	now := time.Now()
	for i := 37; i >= 1; i-- {
		d := 2*time.Minute + 4425*time.Millisecond
		if i <= 7 {
			d = 3*time.Minute + 6864*time.Millisecond
		}
		run, err := s.CreateRun(ctx, job.ID, now.Add(-time.Duration(i)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CompleteRun(ctx, run.ID, storage.Completion{Ended: run.StartedAt.Add(d), Duration: d, Status: "success", ExitCode: &zero}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	var out bytes.Buffer
	err = Run(ctx, []string{"check", "--data-dir", dir}, &out, &bytes.Buffer{})
	if exit, ok := errors.AsType[*ExitError](err); !ok || exit.Code != 1 {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out.String(), "WARNING: Export: 3m7s over the last 7 days, up 50% from 2m4s") {
		t.Fatalf("output = %q", out.String())
	}
}
