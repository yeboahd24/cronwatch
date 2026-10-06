package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if !strings.Contains(errOut.String(), "--on-failure hook for job failed: exit status 9 (attempt 1 of 12, retrying in 1m)\nhook-output") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// openStore opens a store in dir for a test.
func openStore(t *testing.T, dir string) *storage.Store {
	t.Helper()
	s, err := storage.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// makeAlertsDue makes every pending alert due now, as if its retry delay had passed.
func makeAlertsDue(t *testing.T, s *storage.Store) {
	t.Helper()
	if _, err := s.DB.Exec("UPDATE alerts SET next_attempt_at = ? WHERE status = 'pending'", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func TestFailedAlertIsRetriedOnTheNextRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hook, events := hookLog(t)
	run := func(extra ...string) {
		args := append([]string{"run", "--name", "job", "--data-dir", dir}, extra...)
		if err := Run(ctx, append(args, "--", "false"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			if _, ok := errors.AsType[*ExitError](err); !ok {
				t.Fatal(err)
			}
		}
	}
	run("--on-failure", "echo cannot reach the mail server; exit 1")
	s := openStore(t, dir)
	job, err := s.GetJobBySlug(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	alerts, err := s.JobAlerts(ctx, job.ID, 10)
	if err != nil || len(alerts) != 1 {
		t.Fatalf("alerts = %+v, %v", alerts, err)
	}
	a := alerts[0]
	if a.Status != storage.AlertPending || a.Attempts != 1 || a.LastError != "exit status 1" || a.LastOutput != "cannot reach the mail server" || a.NextAttemptAt == nil || a.RunID == "" {
		t.Fatalf("alert after a failed attempt = %+v", a)
	}

	// Another failing run raises no new alert, and the first is not due yet.
	run()
	if alerts, _ := s.JobAlerts(ctx, job.ID, 10); len(alerts) != 1 || alerts[0].Attempts != 1 {
		t.Fatalf("alerts before the retry is due = %+v", alerts)
	}

	// Once it is due, the next run retries it with the job's fixed hook.
	makeAlertsDue(t, s)
	run("--on-failure", hook)
	alerts, _ = s.JobAlerts(ctx, job.ID, 10)
	if len(alerts) != 1 || alerts[0].Status != storage.AlertDelivered || alerts[0].Attempts != 2 || alerts[0].LastError != "" {
		t.Fatalf("alert after the retry = %+v", alerts)
	}
	// The retry passes the event of the run that raised it.
	if got := events(); len(got) != 1 || got[0] != "failed job failed 1" {
		t.Fatalf("events = %q", got)
	}
}

func TestAlertDelivery(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	hook, events := hookLog(t)
	broken := "exit 1"
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "job", Name: "Job", Command: `"true"`, OnFailure: &broken, OnRecover: &hook})
	if err != nil {
		t.Fatal(err)
	}
	queue := func(hook, kind string) {
		queueAlert(ctx, s, hook, hookEvent{Kind: kind, Job: job, ExpectedAt: time.Now()}, &bytes.Buffer{})
	}
	statuses := func() string {
		alerts, err := s.JobAlerts(ctx, job.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range slices.Backward(alerts) {
			out = append(out, fmt.Sprintf("%s:%s:%d", a.Event, a.Status, a.Attempts))
		}
		return strings.Join(out, " ")
	}

	// A failing alert holds back the job's later alerts until it is settled.
	queue("on_failure", "missed")
	queue("on_recover", "recovered")
	var errOut bytes.Buffer
	deliverAlerts(ctx, s, "", &errOut)
	if got := statuses(); got != "missed:pending:1 recovered:pending:0" {
		t.Fatalf("after the first pass: %s", got)
	}
	for range alertMaxAttempts {
		makeAlertsDue(t, s)
		deliverAlerts(ctx, s, "", &errOut)
	}
	if got := statuses(); got != "missed:undelivered:12 recovered:delivered:1" {
		t.Fatalf("after retries: %s\n%s", got, errOut.String())
	}
	if !strings.Contains(errOut.String(), "attempt 12 of 12, giving up") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if got := events(); len(got) != 1 || got[0] != "recovered job missed" {
		t.Fatalf("events = %q", got)
	}

	// An alert whose hook was removed is cancelled rather than retried.
	queue("on_failure", "missed")
	if _, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "job", Name: "Job", Command: `"true"`, OnFailure: new("")}); err != nil {
		t.Fatal(err)
	}
	deliverAlerts(ctx, s, job.ID, &errOut)
	if got := statuses(); !strings.HasSuffix(got, " missed:cancelled:0") {
		t.Fatalf("after removing the hook: %s", got)
	}
}

func TestAlertIsClaimedOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "job", Name: "Job", Command: `"true"`, OnFailure: new("true")})
	if err != nil {
		t.Fatal(err)
	}
	queueAlert(ctx, s, "on_failure", hookEvent{Kind: "missed", Job: job, ExpectedAt: time.Now()}, &bytes.Buffer{})
	pending, err := s.PendingAlerts(ctx, "")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	now := time.Now()
	first, err1 := s.ClaimAlert(ctx, pending[0].ID, now, now.Add(time.Minute))
	second, err2 := s.ClaimAlert(ctx, pending[0].ID, now, now.Add(time.Minute))
	if !first || second || err1 != nil || err2 != nil {
		t.Fatalf("claims = %v, %v (%v, %v); want only the first to succeed", first, second, err1, err2)
	}
}

func TestHookOutputIsBounded(t *testing.T) {
	out, err := runHook("yes 0123456789 | head -c 1000000; echo; echo the end", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > hookOutputLimit+len("…") || !strings.HasPrefix(out, "…") || !strings.HasSuffix(out, "\nthe end") {
		t.Fatalf("output = %d bytes, ending %q", len(out), out[max(0, len(out)-40):])
	}
	b := &tailBuffer{limit: 4}
	for _, w := range []string{"ab", "cdef", "g", "hijklmnop", "q"} {
		_, _ = b.Write([]byte(w))
		if len(b.buf) > 8 {
			t.Fatalf("buffer grew to %d bytes", len(b.buf))
		}
	}
	if got := b.String(); got != "…nopq" {
		t.Fatalf("tail = %q", got)
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

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Minute: "1m", time.Hour: "1h", 90 * time.Minute: "1h30m", 20 * time.Second: "20s", 0: "0s", 61 * time.Second: "1m1s"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
