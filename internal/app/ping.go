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
	"github.com/yeboahd24/cronwatch/internal/hub"
	"github.com/yeboahd24/cronwatch/internal/model"
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
	every := fs.Duration("every", 0, "expect a ping at least once every `duration`, such as 1h, instead of on a cron schedule")
	grace := fs.Duration("grace", storage.DefaultGrace, "how late a run may start before it counts as missed")
	var tags stringList
	fs.Var(&tags, "tag", "`tag` to group the job by; repeat or separate with commas for more, and pass \"\" to remove them")
	maxDuration := fs.Duration("max-duration", 0, "how long a run started with --start may go without its end ping before it is recorded as timed out (0 removes the limit)")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("ping requires one job slug")
	}
	p := pingRequest{Slug: fs.Arg(0), Start: *start, Fail: *fail, ExitCode: *exit, Message: *message}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			p.Name = name
		case "schedule":
			p.Schedule = expr
		case "every":
			p.Every = every
		case "grace":
			p.Grace = grace
		case "max-duration":
			p.MaxDuration = maxDuration
		}
	})
	if tags != nil {
		p.Tags = []string(tags)
	}
	if err := p.check(); err != nil {
		return err
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
	_, err = recordPing(ctx, lifecycle{s: s, stderr: stderr}, p)
	return err
}

// pingRequest is one ping, from cronwatch ping or a hub's ping endpoint.
// Nil fields leave the job's settings as they are.
type pingRequest struct {
	Slug     string
	Start    bool
	Fail     bool
	ExitCode int // -1 for the default: 0, or 1 with Fail
	Message  string

	Name        *string
	Schedule    *string
	Every       *time.Duration // sets the schedule to "@every"; not with Schedule
	Grace       *time.Duration
	MaxDuration *time.Duration
	Tags        []string // nil leaves them; empty removes them
}

// check reports what is wrong with p, before anything is recorded.
func (p pingRequest) check() error {
	if !validSlug(p.Slug) {
		return errors.New("job slug must contain lowercase letters, digits, or hyphens")
	}
	if p.Start && (p.Fail || p.ExitCode >= 0 || p.Message != "") {
		return errors.New("--start takes no --fail, --exit-code or --message; pass them to the end ping")
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) == "" {
		return errors.New("--name must not be empty")
	}
	if p.Schedule != nil && *p.Schedule != "" {
		if err := schedule.Validate(*p.Schedule, time.Now()); err != nil {
			return err
		}
	}
	if p.Every != nil {
		if p.Schedule != nil {
			return errors.New("--every and --schedule cannot go together: a job is expected either every period or on a cron schedule")
		}
		if err := schedule.Validate(schedule.Every(*p.Every), time.Now()); err != nil {
			return fmt.Errorf("--every: %w", err)
		}
	}
	if p.Grace != nil && *p.Grace < 0 {
		return errors.New("--grace must not be negative")
	}
	if d := p.MaxDuration; d != nil && (*d < 0 || (*d > 0 && *d < time.Second)) {
		return errors.New("--max-duration must be 0 or at least 1s")
	}
	if _, err := model.Tags(p.Tags); err != nil {
		return fmt.Errorf("--%w", err)
	}
	return nil
}

// recordPing records p, which check has accepted, creating its job if
// needed. The job's hooks come from this process's environment, as for
// cronwatch run without hook flags.
func recordPing(ctx context.Context, runs lifecycle, p pingRequest) (model.Job, error) {
	s := runs.s
	// An existing job keeps its name and command unless a name is given.
	spec := storage.JobSpec{Slug: p.Slug, Name: p.Slug, Schedule: p.Schedule, Grace: p.Grace, MaxDuration: p.MaxDuration}
	if p.Every != nil {
		expr := schedule.Every(*p.Every)
		spec.Schedule = &expr
	}
	if existing, err := s.GetJobBySlug(ctx, p.Slug); err == nil {
		spec.Name, spec.Command = existing.Name, existing.Command
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	if p.Name != nil {
		spec.Name = *p.Name
	}
	if p.Tags != nil {
		tags, _ := model.Tags(p.Tags) // check has accepted them
		spec.Tags = &tags
	}
	for _, h := range []struct {
		dst **string
		env string
	}{{&spec.OnFailure, envOnFailure}, {&spec.OnRecover, envOnRecover}} {
		v, ok, err := envHook(os.LookupEnv, h.env)
		if err != nil {
			fmt.Fprintf(runs.stderr, "cronwatch: warning: %v\n", err)
		} else if ok {
			*h.dst = &v
		}
	}
	j, err := s.UpsertJob(ctx, spec)
	if err != nil {
		return model.Job{}, err
	}

	now := time.Now().UTC()
	// A run past its --max-duration has timed out, whether or not another
	// process noticed before this ping did.
	runs.expireHeartbeats(ctx, j.ID, now)
	open, err := s.OpenHeartbeatRun(ctx, j.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return j, err
	case p.Start:
		// The previous run never got its end ping.
		note := "cronwatch: no end ping before the next start ping\n"
		code := 1
		if _, err := runs.finish(ctx, j, open.ID, storage.Completion{Ended: now, Duration: now.Sub(open.StartedAt), Status: "failed",
			ExitCode: &code, Stderr: note, Combined: "\x02" + note, Reason: "no end ping before the next --start"}); err != nil {
			return j, err
		}
	}
	if p.Start {
		_, err := runs.start(ctx, j, now, true)
		return j, err
	}

	run := open
	if run.ID == "" {
		if run, err = runs.start(ctx, j, now, true); err != nil {
			return j, err
		}
	}
	status, code := "success", 0
	if p.Fail {
		status, code = "failed", 1
	}
	if p.ExitCode >= 0 {
		code = p.ExitCode
	}
	c := storage.Completion{Ended: now, Duration: now.Sub(run.StartedAt), Status: status, ExitCode: &code}
	if msg := strings.TrimRight(p.Message, "\n"); msg != "" {
		if p.Fail {
			c.Stderr = msg + "\n"
			c.Combined = "\x02" + strings.ReplaceAll(msg, "\n", "\n\x02") + "\n"
		} else {
			c.Stdout, c.Combined = msg+"\n", msg+"\n"
		}
	}
	if _, err := runs.finish(ctx, j, run.ID, c); err != nil {
		return j, fmt.Errorf("record ping: %w", err)
	}
	return j, nil
}

// hubPings records pings that arrive at a hub as this machine's own, like
// cronwatch ping. Alerts they raise are delivered after the response, until
// ctx, serve's, is done.
func hubPings(ctx context.Context, s *storage.Store, stderr io.Writer) hub.PingFunc {
	return func(reqCtx context.Context, hp hub.Ping) error {
		p := pingRequest{Slug: hp.Slug, Start: hp.Start, Fail: hp.Fail, ExitCode: hp.ExitCode, Message: hp.Message,
			Name: hp.Name, Schedule: hp.Schedule, Every: hp.Every, Grace: hp.Grace, MaxDuration: hp.MaxDuration, Tags: hp.Tags}
		if err := p.check(); err != nil {
			return fmt.Errorf("%w: %v", hub.ErrBadPing, err)
		}
		job, err := recordPing(reqCtx, lifecycle{s: s, stderr: stderr, remote: true}, p)
		if err != nil {
			fmt.Fprintf(stderr, "hub: could not record %s's ping of %s: %v\n", hp.From, hp.Slug, err)
			return err
		}
		go deliverAlerts(ctx, s, job.ID, stderr)
		return nil
	}
}
