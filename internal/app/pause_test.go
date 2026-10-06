package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPauseResumeArchive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hook, events := hookLog(t)
	cw := func(args ...string) (string, error) {
		t.Helper()
		var out bytes.Buffer
		err := Run(ctx, append([]string{args[0], "--data-dir", dir}, args[1:]...), &out, &bytes.Buffer{})
		return out.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := cw(args...)
		if _, isExit := errors.AsType[*ExitError](err); err != nil && !isExit {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	must("run", "--name", "Nightly", "--schedule", "0 * * * *", "--grace", "0s", "--on-failure", hook, "--", "true")
	must("run", "--name", "Other", "--", "true")
	s := openStore(t, dir)
	backdate := func() {
		t.Helper()
		// As if the job had last been checked six hours ago.
		if _, err := s.DB.ExecContext(ctx, "UPDATE jobs SET missed_checked_until = ? WHERE slug = 'nightly'", time.Now().Add(-6*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	status := func(slug string) string {
		t.Helper()
		job, err := s.GetJobBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		v, err := s.JobView(ctx, job, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return v.Status
	}

	// A typo in any slug changes nothing.
	if _, err := cw("pause", "nightly", "nope"); err == nil || status("nightly") != "success" {
		t.Fatalf("pause with an unknown slug: %v, status %s", err, status("nightly"))
	}
	if out := must("pause", "nightly"); out != "Paused Nightly until you resume it.\n" {
		t.Fatalf("pause: %q", out)
	}
	// While paused: no missed runs, no alerts, runs still recorded.
	backdate()
	must("run", "--name", "Nightly", "--", "false")
	if n, err := s.DetectMissed(ctx, time.Now()); err != nil || n != 0 || status("nightly") != "paused" {
		t.Fatalf("missed while paused = %d, %v; status %s", n, err, status("nightly"))
	}
	if e := events(); len(e) != 1 || e[0] != "" {
		t.Fatalf("alerts while paused = %q", e)
	}
	if out, err := cw("check"); err != nil || !strings.HasPrefix(out, "CRONWATCH OK - 1 job ok, 1 paused or archived |") {
		t.Fatalf("check while paused: %v\n%s", err, out)
	}
	if out := must("jobs"); !strings.Contains(out, "Nightly  paused") {
		t.Fatalf("jobs:\n%s", out)
	}

	// Resuming checks missed runs from now, not over the pause.
	if out := must("resume", "nightly"); out != "Resumed Nightly.\n" {
		t.Fatalf("resume: %q", out)
	}
	if n, err := s.DetectMissed(ctx, time.Now()); err != nil || n != 0 || status("nightly") != "failed" {
		t.Fatalf("missed after resume = %d, %v; status %s", n, err, status("nightly"))
	}
	if out := must("resume", "nightly"); out != "Nightly is not paused or archived.\n" {
		t.Fatalf("second resume: %q", out)
	}

	// A pause for a while ends by itself, and the paused time is not missed.
	must("run", "--name", "Nightly", "--", "true")
	if out := must("pause", "--for", "2h", "nightly"); !strings.HasPrefix(out, "Paused Nightly until ") {
		t.Fatalf("pause --for: %q", out)
	}
	if out := must("jobs", "--json"); !strings.Contains(out, `"paused_until": "20`) {
		t.Fatalf("jobs --json lacks paused_until:\n%s", out)
	}
	ended := time.Now().Add(-time.Minute)
	if _, err := s.DB.ExecContext(ctx, "UPDATE jobs SET paused_until = ? WHERE slug = 'nightly'", ended.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	backdate()
	if n, err := s.DetectMissed(ctx, time.Now()); err != nil || n != 0 || status("nightly") != "success" {
		t.Fatalf("missed after the pause ran out = %d, %v; status %s", n, err, status("nightly"))
	}
	// Alerts are raised again.
	must("run", "--name", "Nightly", "--", "false")
	if e := events(); len(e) != 1 || !strings.HasPrefix(e[0], "failed nightly failed 1") {
		t.Fatalf("alerts after the pause = %q", e)
	}

	// Archived jobs are kept but left off lists unless asked for.
	if out := must("archive", "nightly"); !strings.HasPrefix(out, "Archived Nightly.") {
		t.Fatalf("archive: %q", out)
	}
	if out := must("jobs"); strings.Contains(out, "Nightly") {
		t.Fatalf("jobs lists an archived job:\n%s", out)
	}
	if out := must("jobs", "--all"); !strings.Contains(out, "Nightly  archived") {
		t.Fatalf("jobs --all:\n%s", out)
	}
	if out, err := cw("check"); err != nil || !strings.HasPrefix(out, "CRONWATCH OK - 1 job ok |") {
		t.Fatalf("check leaves archived jobs out: %v\n%s", err, out)
	}
	if out, err := cw("check", "nightly"); err != nil || !strings.HasPrefix(out, "CRONWATCH OK - 0 jobs ok, 1 paused or archived |") {
		t.Fatalf("check of a named archived job: %v\n%s", err, out)
	}
	if _, err := cw("pause", "nightly"); err == nil || !strings.Contains(err.Error(), "archived; resume it first") {
		t.Fatalf("pause of an archived job: %v", err)
	}
	if out := must("resume", "nightly"); out != "Resumed Nightly.\n" || status("nightly") != "failed" {
		t.Fatalf("resume of an archived job: %q, status %s", out, status("nightly"))
	}
	for _, bad := range [][]string{{"pause"}, {"pause", "--for", "-1h", "nightly"}, {"archive"}} {
		if _, err := cw(bad...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
