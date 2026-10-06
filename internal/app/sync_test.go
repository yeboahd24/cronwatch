package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/crontab"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

const testCrontab = `CW=/home/u/.local/bin/cronwatch
0 2 * * * cd /srv/app && $CW run --name "DB backup" --schedule "0 2 * * *" --grace 10m -- bash scripts/backup.sh >> /var/log/backup.log 2>&1
*/5 * * * * $CW run --name "Ingest queue" -- bash ingest.sh >> /tmp/ingest.log 2>&1
30 4 * * 0 docker builder prune -af >> ~/prune.log 2>&1
0 3 * * * $CW run --name "Bad" --schedule "every day" -- true
0 4 * * * $CW run --name "DB backup" -- other.sh
@reboot $CW serve --addr 127.0.0.1:8765
`

func TestSyncCrontab(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	result, err := syncCrontab(ctx, s, testCrontab)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Added, ",") != "DB backup,Ingest queue" || len(result.Updated) != 0 || result.Jobs != 2 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Unmonitored) != 1 || result.Unmonitored[0].Line != 4 {
		t.Fatalf("unmonitored = %+v", result.Unmonitored)
	}
	if len(result.Problems) != 2 || !strings.Contains(result.Problems[0], "invalid cron") || !strings.Contains(result.Problems[1], "also used on line 2") {
		t.Fatalf("problems = %q", result.Problems)
	}

	backup, err := s.GetJobBySlug(ctx, "db-backup")
	if err != nil {
		t.Fatal(err)
	}
	if *backup.Schedule != "0 2 * * *" || backup.GraceSeconds != 600 || backup.Command != `"bash" "scripts/backup.sh"` {
		t.Fatalf("backup = %+v", backup)
	}
	// Without --schedule, the crontab line's schedule is used.
	ingest, err := s.GetJobBySlug(ctx, "ingest-queue")
	if err != nil {
		t.Fatal(err)
	}
	if ingest.Schedule == nil || *ingest.Schedule != "*/5 * * * *" {
		t.Fatalf("ingest = %+v", ingest)
	}
	views, err := s.ListJobViews(ctx, backup.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Status != "never_run" || v.NextExpectedAt == nil {
			t.Fatalf("view = %+v", v)
		}
	}

	// A second sync changes nothing.
	if result, err := syncCrontab(ctx, s, testCrontab); err != nil || len(result.Added)+len(result.Updated) != 0 {
		t.Fatalf("second sync = %+v, %v", result, err)
	}
	// Changing the schedule in the crontab updates the job.
	changed := strings.Replace(testCrontab, `--schedule "0 2 * * *"`, `--schedule "30 2 * * *"`, 1)
	if result, err := syncCrontab(ctx, s, changed); err != nil || strings.Join(result.Updated, ",") != "DB backup" {
		t.Fatalf("schedule change = %+v, %v", result, err)
	}
}

func TestSyncedJobMatchesRealRun(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	tab := filepath.Join(dir, "crontab")
	line := `*/5 * * * * cronwatch run --name "Ingest queue" --data-dir ` + dir + ` -- sh -c 'echo ok'` + "\n"
	if err := os.WriteFile(tab, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(ctx, []string{"sync", "--data-dir", dir, "--crontab", tab}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Added    Ingest queue") {
		t.Fatalf("sync output = %q", out.String())
	}
	if err := Run(ctx, []string{"run", "--name", "Ingest queue", "--data-dir", dir, "--", "sh", "-c", "echo ok"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || *jobs[0].Schedule != "*/5 * * * *" || jobs[0].Command != `"sh" "-c" "echo ok"` {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestPrintSyncResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   syncResult
		want string
	}{
		{"up to date", syncResult{Jobs: 8}, "8 jobs in the crontab, all registered. Every crontab line is monitored.\n"},
		{"added one", syncResult{Jobs: 1, Added: []string{"Backup"}}, "Added    Backup\n\n1 job in the crontab, all registered. Every crontab line is monitored.\n"},
		{"empty", syncResult{}, "No crontab lines use cronwatch run.\n"},
		{"problems and unmonitored", syncResult{Jobs: 2, Problems: []string{"line 4: bad"}, Unmonitored: []crontab.Entry{{Line: 7, Raw: "@daily", Command: "backup.sh"}}},
			"2 of 3 jobs in the crontab registered.\n\nCould not read 1 cronwatch line:\n  line 4: bad\n\nNot monitored (1 line without cronwatch run):\n  line 7: @daily backup.sh\n"},
	} {
		var out bytes.Buffer
		printSyncResult(&out, tc.in)
		if out.String() != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, out.String(), tc.want)
		}
	}
}

// fakeCrontab puts a crontab command on PATH that keeps the user's crontab
// in a file, and returns a function that sets its content.
func fakeCrontab(t *testing.T) func(string) {
	t.Helper()
	bin, store := t.TempDir(), filepath.Join(t.TempDir(), "tab")
	script := "#!/bin/sh\ncase \"$1\" in\n-l) cat " + store + " ;;\n-) cat > " + store + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	return func(text string) {
		if err := os.WriteFile(store, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncUnschedulesJobsRemovedFromCrontab(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	setCrontab := fakeCrontab(t)
	sync := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := Run(ctx, append([]string{"sync", "--data-dir", dir}, args...), &out, &bytes.Buffer{}); err != nil {
			t.Fatalf("sync %v: %v", args, err)
		}
		return out.String()
	}
	schedule := func(slug string) string {
		t.Helper()
		s := openStore(t, dir)
		job, err := s.GetJobBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		if job.Schedule == nil {
			return ""
		}
		return *job.Schedule
	}
	both := "0 1 * * * cronwatch run --name A -- true\n0 2 * * * cronwatch run --name B -- true\n"
	setCrontab(both)
	sync()
	// A job scheduled by hand, not by the crontab, is left alone.
	if err := Run(ctx, []string{"run", "--data-dir", dir, "--name", "Manual", "--schedule", "0 3 * * *", "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	setCrontab("0 1 * * * cronwatch run --name A -- true\n")
	if out := sync(); !strings.Contains(out, "Unscheduled  B (no longer in the crontab)") {
		t.Fatalf("sync output:\n%s", out)
	}
	if a, b, m := schedule("a"), schedule("b"), schedule("manual"); a != "0 1 * * *" || b != "" || m != "0 3 * * *" {
		t.Fatalf("schedules = %q, %q, %q", a, b, m)
	}
	// An unscheduled job is not reported as missed.
	s := openStore(t, dir)
	if _, err := s.DB.ExecContext(ctx, "UPDATE jobs SET created_at = ?, missed_checked_until = NULL WHERE slug = 'b'", time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	missed, err := s.DetectMissedJobs(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range missed {
		if m.Job.Slug == "b" {
			t.Fatalf("B was reported missed at %v", m.ExpectedAt)
		}
	}

	// A draft file does not say what cron runs.
	draft := filepath.Join(t.TempDir(), "draft")
	if err := os.WriteFile(draft, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := sync("--crontab", draft); strings.Contains(out, "Unscheduled") || schedule("a") != "0 1 * * *" {
		t.Fatalf("draft sync unscheduled A:\n%s", out)
	}

	// While a cronwatch line cannot be read, it may be A's, so A is kept.
	setCrontab("0 1 * * * cronwatch run --name A --schedule 'never' -- true\n")
	if out := sync(); strings.Contains(out, "Unscheduled") || schedule("a") != "0 1 * * *" {
		t.Fatalf("sync with an unreadable line:\n%s", out)
	}
	setCrontab("")
	if out := sync(); !strings.Contains(out, "Unscheduled  A") || schedule("a") != "" {
		t.Fatalf("sync of an empty crontab:\n%s", out)
	}
	// A line that comes back brings its schedule with it.
	setCrontab(both)
	if out := sync(); !strings.Contains(out, "Updated  A") || schedule("a") != "0 1 * * *" || schedule("b") != "0 2 * * *" {
		t.Fatalf("sync after restoring the lines:\n%s", out)
	}
	if out := sync(); strings.Contains(out, "Unscheduled") || strings.Contains(out, "Updated") {
		t.Fatalf("a second sync changed something:\n%s", out)
	}
}
