package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

// hookLog returns a hook command that appends one line per event to a file,
// and a function reading the lines written so far.
func hookLog(t *testing.T) (string, func() []string) {
	path := filepath.Join(t.TempDir(), "events")
	cmd := `echo "$CRONWATCH_EVENT $CRONWATCH_JOB_SLUG $CRONWATCH_STATUS $CRONWATCH_EXIT_CODE $CRONWATCH_LAST_ERROR" >> ` + path
	return cmd, func() []string {
		data, _ := os.ReadFile(path)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func TestHooksFireOnStateChanges(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hook, events := hookLog(t)
	run := func(script string, extra ...string) {
		args := append([]string{"run", "--name", "job", "--data-dir", dir}, extra...)
		err := Run(ctx, append(args, "--", "sh", "-c", script), &bytes.Buffer{}, &bytes.Buffer{})
		if _, isExit := errors.AsType[*ExitError](err); err != nil && !isExit {
			t.Fatal(err)
		}
	}
	run("true", "--on-failure", hook, "--on-recover", hook) // ok: nothing to report
	run("echo boom >&2; exit 3")                            // hooks are kept from the first run
	run("exit 4")                                           // still failing: no second alert
	run("true")                                             // recovered
	run("true")                                             // still ok
	want := []string{"failed job failed 3 boom", "recovered job success 0"}
	if got := events(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("events = %q, want %q", got, want)
	}

	// A broken hook is reported but does not change the job's result.
	var errOut bytes.Buffer
	err := Run(ctx, []string{"run", "--name", "job", "--data-dir", dir, "--on-failure", "echo hook-output; exit 9", "--", "false"}, &bytes.Buffer{}, &errOut)
	if exit, ok := errors.AsType[*ExitError](err); !ok || exit.Code != 1 {
		t.Fatalf("run with a broken hook: %v", err)
	}
	if !strings.Contains(errOut.String(), "--on-failure hook for job failed: exit status 9\nhook-output") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestHookFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	hook, events := hookLog(t)
	t.Setenv(envOnFailure, hook)
	err := Run(context.Background(), []string{"run", "--name", "env-job", "--data-dir", dir, "--timeout", "100ms", "--", "sleep", "5"}, &bytes.Buffer{}, &bytes.Buffer{})
	if _, ok := errors.AsType[*ExitError](err); !ok {
		t.Fatal(err)
	}
	if got := events(); len(got) != 1 || !strings.HasPrefix(got[0], "timeout env-job timeout 143") {
		t.Fatalf("events = %q", got)
	}
}

func TestMissedRunFiresHookOnce(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hook, events := hookLog(t)
	expr, grace := "* * * * *", time.Duration(0)
	if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "minutely", Name: "Minutely", Command: `"true"`, Schedule: &expr, Grace: &grace, OnFailure: &hook}); err != nil {
		t.Fatal(err)
	}
	// Backdate the job so several minutes have been missed.
	if _, err := s.DB.ExecContext(ctx, "UPDATE jobs SET created_at = ?, missed_checked_until = NULL", time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	for range 2 {
		if err := maintain(ctx, s, &errOut); err != nil {
			t.Fatal(err)
		}
	}
	if got := events(); len(got) != 1 || got[0] != "missed minutely missed" {
		t.Fatalf("events = %q, stderr = %q", got, errOut.String())
	}
}

func TestSyncTakesHooksFromCrontab(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tab := "CRONWATCH_ON_FAILURE=notify-send failed\n" +
		"* * * * * cronwatch run --name A -- true\n" +
		"* * * * * cronwatch run --name B --on-failure 'mail me' -- true\n"
	if _, err := syncCrontab(ctx, s, tab); err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]string{"a": "notify-send failed", "b": "mail me"} {
		if job, err := s.GetJobBySlug(ctx, slug); err != nil || job.OnFailure != want {
			t.Fatalf("%s: on_failure = %q, %v; want %q", slug, job.OnFailure, err, want)
		}
	}
	// Removing the variable from the crontab removes the hook.
	result, err := syncCrontab(ctx, s, strings.SplitN(tab, "\n", 2)[1])
	if err != nil || len(result.Updated) != 1 {
		t.Fatalf("resync: %+v, %v", result, err)
	}
	if job, _ := s.GetJobBySlug(ctx, "a"); job.OnFailure != "" {
		t.Fatalf("hook kept after removal: %q", job.OnFailure)
	}
}
