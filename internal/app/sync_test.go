package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
