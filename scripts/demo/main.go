// Command demo fills a CronWatch data directory with realistic, made-up
// history for screenshots and demos. It writes through the storage package,
// so failure signatures, missed runs and the rest come from the real code.
//
//	go run ./scripts/demo -data-dir /tmp/cronwatch-demo
//
// It prints KEY=VALUE lines naming the runs and jobs worth showing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

const day = 24 * time.Hour

type demo struct {
	ctx context.Context
	s   *storage.Store
	rng *rand.Rand
	now time.Time
}

func main() {
	dir := flag.String("data-dir", "", "data directory to fill; must not exist yet")
	flag.Parse()
	if *dir == "" {
		log.Fatal("-data-dir is required")
	}
	if _, err := os.Stat(*dir); err == nil {
		log.Fatalf("%s exists; the demo only fills a new directory", *dir)
	}
	ctx := context.Background()
	s, err := storage.Open(ctx, *dir)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	d := &demo{ctx: ctx, s: s, rng: rand.New(rand.NewPCG(4, 2)), now: time.Now().UTC().Truncate(time.Minute)}
	if err := d.fill(); err != nil {
		log.Fatal(err)
	}
}

// cron and cronWithPgsql are the environments cron gives the jobs before and
// after someone removes the PATH line from the crontab.
var (
	cronWithPgsql = runenv.Env{Dir: "/home/deploy", User: "deploy", UID: "1001",
		Vars:  map[string]string{"HOME": "/home/deploy", "LOGNAME": "deploy", "PATH": "/usr/local/pgsql/bin:/usr/bin:/bin", "SHELL": "/bin/sh"},
		Names: []string{"HOME", "LOGNAME", "MAILTO", "PATH", "PGPASSFILE", "SHELL"}}
	cron = runenv.Env{Dir: "/home/deploy", User: "deploy", UID: "1001",
		Vars:  map[string]string{"HOME": "/home/deploy", "LOGNAME": "deploy", "PATH": "/usr/bin:/bin", "SHELL": "/bin/sh"},
		Names: []string{"HOME", "LOGNAME", "MAILTO", "PATH", "SHELL"}}
)

// run records one finished run.
type run struct {
	start          time.Time
	took           time.Duration
	status         string
	code           int
	stdout, stderr string
	reason         string
	env            runenv.Env
	overlaps       string
}

func (d *demo) job(slug, name, schedule string, created time.Time) model.Job {
	grace := 10 * time.Minute
	j, err := d.s.UpsertJob(d.ctx, storage.JobSpec{Slug: slug, Name: name, Command: `"/home/deploy/bin/` + slug + `.sh"`, Schedule: &schedule, Grace: &grace})
	if err != nil {
		log.Fatal(err)
	}
	// Jobs are registered as of now; backdate them so their history counts.
	if _, err := d.s.DB.ExecContext(d.ctx, "UPDATE jobs SET created_at = ?, missed_checked_until = NULL WHERE id = ?",
		created.Format("2006-01-02T15:04:05.000000000Z"), j.ID); err != nil {
		log.Fatal(err)
	}
	return j
}

func (d *demo) record(j model.Job, r run) string {
	created, err := d.s.CreateRun(d.ctx, j.ID, r.start)
	if err != nil {
		log.Fatal(err)
	}
	if r.env.Dir == "" {
		r.env = cronWithPgsql
	}
	if err := d.s.SetRunEnv(d.ctx, created.ID, r.env); err != nil {
		log.Fatal(err)
	}
	var combined strings.Builder
	combined.WriteString(r.stdout)
	for line := range strings.SplitSeq(strings.TrimSuffix(r.stderr, "\n"), "\n") {
		if line != "" {
			combined.WriteString("\x02" + line + "\n")
		}
	}
	code := r.code
	usage := &model.Usage{MaxRSSKB: int64(30000 + d.rng.IntN(20000)), UserCPUMS: r.took.Milliseconds() / 3, SysCPUMS: r.took.Milliseconds() / 40}
	if err := d.s.CompleteRun(d.ctx, created.ID, storage.Completion{Ended: r.start.Add(r.took), Duration: r.took, Status: r.status,
		ExitCode: &code, Stdout: r.stdout, Stderr: r.stderr, Combined: combined.String(), Reason: r.reason, Usage: usage}); err != nil {
		log.Fatal(err)
	}
	if r.overlaps != "" {
		if err := d.s.SetRunOverlap(d.ctx, created.ID, r.overlaps); err != nil {
			log.Fatal(err)
		}
	}
	return created.ID
}

func (d *demo) jitter(base time.Duration, spread float64) time.Duration {
	return time.Duration(float64(base) * (1 + spread*(d.rng.Float64()*2-1)))
}

func (d *demo) fill() error {
	now, today := d.now, d.now.Truncate(day)
	at := func(daysAgo int, hour, minute int) time.Time {
		return today.Add(-time.Duration(daysAgo)*day + time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}

	// Database Backup: steady for weeks, then the crontab's PATH line is
	// removed and tonight's run cannot find pg_dump.
	backup := d.job("database-backup", "Database Backup", "0 2 * * *", at(41, 0, 0))
	for i := 40; i >= 1; i-- {
		stamp := at(i, 2, 0).Format("20060102")
		d.record(backup, run{start: at(i, 2, 0), took: d.jitter(2*time.Minute+40*time.Second, 0.08), status: "success",
			stdout: fmt.Sprintf("Starting backup -> db_%s.sql.gz\npg_dump: dumping 42 tables\nDump complete. Size: 1.%dG\nUploaded db_%s.sql.gz to s3://backups/\n", stamp, 6+i%3, stamp)})
	}
	failed := d.record(backup, run{start: at(0, 2, 0), took: 1200 * time.Millisecond, status: "failed", code: 127, env: cron,
		stdout: fmt.Sprintf("Starting backup -> db_%s.sql.gz\n", at(0, 2, 0).Format("20060102")),
		stderr: "backup.sh: line 12: pg_dump: command not found\n"})

	// Nightly Export: getting slower over the last week, with one very slow
	// night and an old upstream failure.
	export := d.job("nightly-export", "Nightly Export", "0 1 * * *", at(41, 0, 0))
	for i := 40; i >= 0; i-- {
		r := run{start: at(i, 1, 0), took: d.jitter(2*time.Minute, 0.1), status: "success", stdout: "Exported 18,204 rows to reports/daily.csv\n"}
		switch {
		case i == 12:
			r.took, r.status, r.code, r.stdout, r.stderr = 9*time.Second, "failed", 1, "", "export: upstream API returned 503 Service Unavailable\n"
		case i == 3:
			r.took = 14 * time.Minute
		case i <= 7:
			r.took = d.jitter(3*time.Minute, 0.08)
		}
		d.record(export, r)
	}

	// Ingest queue: every 30 minutes for three days, two kinds of failure, an
	// hour when its line was commented out, and a run in progress.
	ingest := d.job("ingest-queue", "Ingest Queue", "*/30 * * * *", now.Add(-3*day))
	gapFrom, gapTo := now.Add(-15*time.Hour), now.Add(-14*time.Hour)
	current := now.Truncate(30 * time.Minute) // the slot whose run is still going
	for t := now.Add(-3 * day).Truncate(30 * time.Minute).Add(30 * time.Minute); t.Before(current); t = t.Add(30 * time.Minute) {
		age := now.Sub(t)
		if !t.Before(gapFrom) && t.Before(gapTo) {
			continue // commented out
		}
		r := run{start: t, took: d.jitter(40*time.Second, 0.6), status: "success", stdout: fmt.Sprintf("Processed %d queued items\n", 20+d.rng.IntN(80))}
		switch {
		case age > 6*time.Hour && age < 7*time.Hour+time.Minute:
			r.status, r.code, r.stderr = "failed", 1, fmt.Sprintf("ERROR: queue lock held by pid %d\n", 4000+d.rng.IntN(900))
		case age > 4*time.Hour && age < 5*time.Hour+time.Minute:
			r.status, r.code, r.stderr = "failed", 2, fmt.Sprintf("psql: error: connection to server at 10.0.0.%d port 5432 failed: Connection refused\n", 10+d.rng.IntN(5))
		}
		if r.status == "failed" {
			r.took, r.stdout = 2*time.Second, ""
		}
		d.record(ingest, r)
	}
	if _, err := d.s.CreateHeartbeatRun(d.ctx, ingest.ID, current); err != nil {
		return err
	}

	// Report Export: every 6 hours, once timing out with the next run
	// skipped by --no-overlap.
	report := d.job("report-export", "Report Export", "0 */6 * * *", at(15, 0, 0))
	for t := at(14, 0, 0); t.Before(now); t = t.Add(6 * time.Hour) {
		if t.Equal(at(1, 6, 0)) {
			slow := d.record(report, run{start: t, took: time.Hour, status: "timeout", code: 143, reason: "timed out after 1h0m0s (--timeout)",
				stdout: "Rendering 14 reports\n", stderr: "render: waiting for chart service\n"})
			d.record(report, run{start: t.Add(20 * time.Minute), status: "skipped", reason: "run " + slow + " was still running (--no-overlap)", overlaps: slow})
			continue
		}
		d.record(report, run{start: t, took: d.jitter(18*time.Minute, 0.3), status: "success", stdout: "Rendering 14 reports\nDone\n"})
	}

	// Docker Prune: weekly and uneventful.
	prune := d.job("docker-prune", "Docker Prune", "30 4 * * 0", at(43, 0, 0))
	for t := at(42, 4, 30); t.Before(now); t = t.Add(day) {
		if t.Weekday() == time.Sunday {
			d.record(prune, run{start: t, took: d.jitter(2*time.Second, 0.2), status: "success", stdout: "Total reclaimed space: 1.2GB\n"})
		}
	}

	// Tile Staleness Check: daily until two days ago.
	tiles := d.job("tile-staleness-check", "Tile Staleness Check", "0 7 * * *", at(21, 0, 0))
	for i := 20; i >= 2; i-- {
		d.record(tiles, run{start: at(i, 7, 0), took: d.jitter(900*time.Millisecond, 0.1), status: "success", stdout: "Tiles built 3 days ago; OK\n"})
	}

	// Crontab history: the baseline, the PATH line removed yesterday, and
	// the ingest line commented out for an hour.
	path := "PATH=/usr/local/pgsql/bin:/usr/bin:/bin"
	ingestLine := `*/30 * * * * $CW run --name "Ingest Queue" -- /home/deploy/bin/ingest-queue.sh`
	for _, snap := range []struct {
		at      time.Time
		changes []storage.CrontabChange
	}{
		{at(42, 12, 0), nil},
		{gapFrom.Add(-2 * time.Minute), []storage.CrontabChange{{JobSlug: "ingest-queue", Kind: "removed", Before: ingestLine}}},
		{gapTo.Add(-2 * time.Minute), []storage.CrontabChange{{JobSlug: "ingest-queue", Kind: "added", After: ingestLine}}},
		{at(1, 16, 42), []storage.CrontabChange{{Kind: "line_removed", Before: path}}},
	} {
		if err := d.s.RecordCrontabSnapshot(d.ctx, storage.CrontabSnapshot{TakenAt: snap.at, Hash: snap.at.String(), Content: "demo"}, snap.changes); err != nil {
			return err
		}
	}

	if _, err := d.s.DetectMissed(d.ctx, now); err != nil {
		return err
	}
	if _, err := d.s.BackfillFailureSignatures(d.ctx, 500); err != nil {
		return err
	}
	fmt.Printf("FAILED_RUN=%s\nEXPORT_JOB=%s\nINGEST_JOB=%s\n", failed, export.ID, ingest.ID)
	return nil
}
