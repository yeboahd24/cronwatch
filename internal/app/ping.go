package app

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/schedule"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func pingCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("ping", "cronwatch ping [flags] JOB-SLUG",
		"Record a run of a job that cannot be wrapped with \"cronwatch run\", such as a step inside a\n"+
			"long script. A ping records a successful run; with --start first, the run lasts from the\n"+
			"start ping to the end ping. --fail records a failure. The job is created if needed.")
	dir := fs.String("data-dir", "", "data directory")
	start := fs.Bool("start", false, "record that the job started; the next ping without --start ends the run")
	fail := fs.Bool("fail", false, "record the run as failed")
	exit := fs.Int("exit-code", -1, "exit code to record (default 0, or 1 with --fail)")
	message := fs.String("message", "", "text to record as the run's output (stderr with --fail)")
	name := fs.String("name", "", "job `name` for a new job (default: the slug)")
	expr := fs.String("schedule", "", "five-field cron `expression` CronWatch should expect the job on")
	grace := fs.Duration("grace", storage.DefaultGrace, "how late a run may start before it counts as missed")
	maxDuration := fs.Duration("max-duration", 0, "how long a run started with --start may go without its end ping before it is recorded as timed out (0 removes the limit)")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("ping requires one job slug")
	}
	slug := fs.Arg(0)
	if !validSlug(slug) {
		return errors.New("job slug must contain lowercase letters, digits, or hyphens")
	}
	if *start && (*fail || *exit >= 0 || *message != "") {
		return errors.New("--start takes no --fail, --exit-code or --message; pass them to the end ping")
	}
	if *expr != "" {
		if err := schedule.Validate(*expr, time.Now()); err != nil {
			return err
		}
	}
	if *maxDuration < 0 || (*maxDuration > 0 && *maxDuration < time.Second) {
		return errors.New("--max-duration must be 0 or at least 1s")
	}
	if *dir == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		*dir = cfg.DataDir
	}
	s, err := storage.Open(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()

	// An existing job keeps its name and command unless --name is passed.
	spec := storage.JobSpec{Slug: slug, Name: slug}
	if existing, err := s.GetJobBySlug(ctx, slug); err == nil {
		spec.Name, spec.Command = existing.Name, existing.Command
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			spec.Name = *name
		case "schedule":
			spec.Schedule = expr
		case "grace":
			spec.Grace = grace
		case "max-duration":
			spec.MaxDuration = maxDuration
		}
	})
	for _, h := range []struct {
		dst **string
		env string
	}{{&spec.OnFailure, envOnFailure}, {&spec.OnRecover, envOnRecover}} {
		if v, ok := os.LookupEnv(h.env); ok {
			*h.dst = &v
		}
	}
	job, err := s.UpsertJob(ctx, spec)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	// A run past its --max-duration has timed out, whether or not another
	// process noticed before this ping did.
	expireHeartbeats(ctx, s, job.ID, now, stderr)
	open, err := s.OpenHeartbeatRun(ctx, job.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case *start:
		// The previous run never got its end ping.
		note := "cronwatch: no end ping before the next start ping\n"
		code := 1
		if err := s.CompleteRun(ctx, open.ID, storage.Completion{Ended: now, Duration: now.Sub(open.StartedAt), Status: "failed",
			ExitCode: &code, Stderr: note, Combined: "\x02" + note, Reason: "no end ping before the next --start"}); err != nil {
			return err
		}
		if finished, err := s.GetRun(ctx, open.ID); err == nil {
			notifyRun(ctx, s, job, finished, stderr)
		}
	}
	if *start {
		run, err := s.CreateHeartbeatRun(ctx, job.ID, now)
		if err != nil {
			return err
		}
		return s.SetRunEnv(ctx, run.ID, runenv.Capture())
	}

	run := open
	if run.ID == "" {
		if run, err = s.CreateHeartbeatRun(ctx, job.ID, now); err != nil {
			return err
		}
		if err := s.SetRunEnv(ctx, run.ID, runenv.Capture()); err != nil {
			return err
		}
	}
	status, code := "success", 0
	if *fail {
		status, code = "failed", 1
	}
	if *exit >= 0 {
		code = *exit
	}
	c := storage.Completion{Ended: now, Duration: now.Sub(run.StartedAt), Status: status, ExitCode: &code}
	if msg := strings.TrimRight(*message, "\n"); msg != "" {
		if *fail {
			c.Stderr = msg + "\n"
			c.Combined = "\x02" + strings.ReplaceAll(msg, "\n", "\n\x02") + "\n"
		} else {
			c.Stdout, c.Combined = msg+"\n", msg+"\n"
		}
	}
	if err := s.CompleteRun(ctx, run.ID, c); err != nil {
		return fmt.Errorf("record ping: %w", err)
	}
	if finished, err := s.GetRun(ctx, run.ID); err == nil {
		notifyRun(ctx, s, job, finished, stderr)
	}
	return nil
}

// expireHeartbeats records as timed out the heartbeat runs of the job with
// jobID, or of every job if jobID is "", that have gone longer than the job's
// --max-duration without an end ping, and queues their alerts.
func expireHeartbeats(ctx context.Context, s *storage.Store, jobID string, now time.Time, stderr io.Writer) {
	expired, err := s.ExpireHeartbeatRuns(ctx, jobID, now)
	if err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not time out heartbeat runs: %v\n", err)
	}
	for _, run := range expired {
		job, err := s.GetJob(ctx, run.JobID)
		if err != nil {
			fmt.Fprintf(stderr, "cronwatch: warning: could not read a timed-out run's job: %v\n", err)
			continue
		}
		notifyRun(ctx, s, job, run, stderr)
	}
}
