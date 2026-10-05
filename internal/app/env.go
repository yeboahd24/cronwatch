package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"strings"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// recordedRun is a run with its recorded environment.
type recordedRun struct {
	Job model.Job
	Run model.Run
	Env runenv.Env
}

// findRecordedRun returns run runID, or the newest run of job slug that has a
// recorded environment. Exactly one of slug and runID is set.
func findRecordedRun(ctx context.Context, s *storage.Store, slug, runID string) (recordedRun, error) {
	var rr recordedRun
	var err error
	if runID != "" {
		if rr.Run, err = s.GetRun(ctx, runID); errors.Is(err, sql.ErrNoRows) {
			return rr, fmt.Errorf("no run with ID %q", runID)
		} else if err != nil {
			return rr, err
		}
		if rr.Job, err = s.GetJob(ctx, rr.Run.JobID); err != nil {
			return rr, err
		}
		if rr.Run.EnvHash == "" {
			return rr, fmt.Errorf("run %s has no recorded environment; it was recorded by an older CronWatch", runID)
		}
	} else {
		if rr.Job, err = s.GetJobBySlug(ctx, slug); errors.Is(err, sql.ErrNoRows) {
			return rr, fmt.Errorf("no job with slug %q", slug)
		} else if err != nil {
			return rr, err
		}
		if rr.Run, err = s.LatestRunWithEnv(ctx, rr.Job.ID); err != nil {
			return rr, err // sql.ErrNoRows: callers explain it
		}
	}
	rr.Env, err = s.GetEnvironment(ctx, rr.Run.EnvHash)
	return rr, err
}

// jobOrRunArgs reads the JOB-SLUG argument or --run flag, of which exactly
// one is required.
func jobOrRunArgs(name string, args []string, runID string) (string, error) {
	switch {
	case len(args) > 1:
		return "", fmt.Errorf("%s accepts one job slug", name)
	case len(args) == 1 && runID != "":
		return "", fmt.Errorf("%s takes a job slug or --run, not both", name)
	case len(args) == 0 && runID == "":
		return "", fmt.Errorf("%s requires a job slug or --run RUN-ID", name)
	case len(args) == 1:
		return args[0], nil
	}
	return "", nil
}

func runLabel(r model.Run) string {
	return fmt.Sprintf("run %s (%s, %s)", r.ID[:min(len(r.ID), 8)], r.StartedAt.Local().Format("2006-01-02 15:04"), r.Status)
}

func envdiffCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("envdiff", "cronwatch envdiff [flags] JOB-SLUG | --run RUN-ID",
		"Compare the environment a job last ran in (usually cron's) with this shell, to explain\n"+
			"why a command works here but fails under cron. Values are recorded only for PATH, HOME,\n"+
			"SHELL, USER, LOGNAME, TZ, TMPDIR and locale variables; others are compared by name.")
	dir := fs.String("data-dir", "", "data directory")
	runID := fs.String("run", "", "compare this run instead of the job's latest")
	lastSuccess := fs.Bool("last-success", false, "compare with the job's last successful run instead of this shell")
	all := fs.Bool("all", false, "also list terminal and desktop session variables such as DISPLAY and XDG_*")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	slug, err := jobOrRunArgs("envdiff", fs.Args(), *runID)
	if err != nil {
		return err
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	rr, err := findRecordedRun(ctx, s, slug, *runID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("job %q has no run with a recorded environment yet", slug)
	} else if err != nil {
		return err
	}
	if !*lastSuccess {
		fmt.Fprintf(stdout, "Comparing %s of %s with this shell.\n\n", runLabel(rr.Run), rr.Job.Name)
		printEnvDiff(stdout, runenv.Compare(rr.Env, runenv.Capture()), "run", "shell", *all)
		return nil
	}
	success, err := s.LastSuccessWithEnvBefore(ctx, rr.Job.ID, rr.Run.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s has no successful run with a recorded environment before %s", rr.Job.Name, runLabel(rr.Run))
	} else if err != nil {
		return err
	}
	successEnv, err := s.GetEnvironment(ctx, success.EnvHash)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Comparing %s of %s with the last success, %s.\n\n", runLabel(rr.Run), rr.Job.Name, runLabel(success))
	printEnvDiff(stdout, runenv.Compare(rr.Env, successEnv), "run", "success", *all)
	return nil
}

// printEnvDiff prints d, naming its two sides a and b. Unless all is set,
// session variables are counted instead of listed.
func printEnvDiff(w io.Writer, d runenv.Diff, a, b string, all bool) {
	session := 0
	if !all {
		d, session = d.WithoutSession()
	}
	if d.Empty() && session == 0 {
		fmt.Fprintln(w, "No differences in the recorded environment.")
		return
	}
	width := max(len(a), len(b)) + 1
	for _, c := range d.Changes {
		fmt.Fprintln(w, c.Field)
		fmt.Fprintf(w, "  %-*s %s\n", width, a+":", orUnset(c.A))
		fmt.Fprintf(w, "  %-*s %s\n", width, b+":", orUnset(c.B))
		if len(c.OnlyB) > 0 {
			fmt.Fprintf(w, "  Missing from %s: %s\n", a, strings.Join(c.OnlyB, ", "))
		}
		if len(c.OnlyA) > 0 {
			fmt.Fprintf(w, "  Missing from %s: %s\n", b, strings.Join(c.OnlyA, ", "))
		}
	}
	if len(d.OnlyB) > 0 {
		fmt.Fprintf(w, "Set only in %s: %s\n", b, strings.Join(d.OnlyB, ", "))
	}
	if len(d.OnlyA) > 0 {
		fmt.Fprintf(w, "Set only in %s: %s\n", a, strings.Join(d.OnlyA, ", "))
	}
	if session > 0 {
		fmt.Fprintf(w, "%s not shown (--all lists them).\n", plural(session, "terminal or desktop session variable", "terminal or desktop session variables"))
	}
	fmt.Fprintln(w, "\nValues of other variables are not recorded, so changes to them are not shown.")
}

func orUnset(v string) string {
	if v == "" {
		return "(unset)"
	}
	return v
}

func tryCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("try", "cronwatch try [flags] JOB-SLUG | --run RUN-ID",
		"Run a job's command now in the environment its latest run had (usually cron's), to\n"+
			"reproduce a failure from a terminal. Only recorded variables are set; a job with no\n"+
			"recorded run gets cron's defaults. The run is not recorded. Exits with the command's code.")
	dir := fs.String("data-dir", "", "data directory")
	runID := fs.String("run", "", "use this run's environment instead of the latest")
	extra := map[string]string{}
	fs.Func("env", "set `NAME=VALUE` as well, e.g. a variable whose value is not recorded (repeatable)", func(v string) error {
		name, value, ok := strings.Cut(v, "=")
		if !ok || name == "" {
			return errors.New("must be NAME=VALUE")
		}
		extra[name] = value
		return nil
	})
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	slug, err := jobOrRunArgs("try", fs.Args(), *runID)
	if err != nil {
		return err
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	rr, err := findRecordedRun(ctx, s, slug, *runID)
	var source string
	switch {
	case errors.Is(err, sql.ErrNoRows):
		rr.Env = runenv.CronDefault()
		source = "cron's default environment (no run has a recorded environment yet)"
	case err != nil:
		return err
	default:
		source = "the environment of " + runLabel(rr.Run)
	}
	rr.Env.Vars = maps.Clone(rr.Env.Vars)
	maps.Copy(rr.Env.Vars, extra)
	argv, err := splitCommand(rr.Job.Command)
	if err != nil {
		return err
	}
	_ = s.Close() // the command may run for a long time

	fmt.Fprintf(stderr, "cronwatch: trying %s with %s\n", rr.Job.Name, source)
	fmt.Fprintf(stderr, "cronwatch: directory %s, PATH=%s\n", rr.Env.Dir, rr.Env.Vars["PATH"])
	if hidden := rr.Env.Hidden(); len(hidden) > 0 && len(hidden) <= 10 {
		fmt.Fprintf(stderr, "cronwatch: not set, because their values are not recorded: %s\n", strings.Join(hidden, ", "))
	} else if len(hidden) > 10 {
		fmt.Fprintf(stderr, "cronwatch: %d variables not set, because their values are not recorded\n", len(hidden))
	}
	if info, err := os.Stat(rr.Env.Dir); err != nil || !info.IsDir() {
		fmt.Fprintf(stderr, "cronwatch: working directory %s does not exist\n", rr.Env.Dir)
		return &ExitError{Code: 1, Err: errors.New("working directory missing")}
	}
	path, ok := rr.Env.LookPath(argv[0])
	if !ok {
		fmt.Fprintf(stderr, "cronwatch: %s: command not found with this PATH\n", argv[0])
		return &ExitError{Code: 127, Err: errors.New("command not found")}
	}
	fmt.Fprintln(stderr)
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Dir = rr.Env.Dir
	cmd.Env = rr.Env.Environ()
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return &ExitError{Code: max(exit.ExitCode(), 1), Err: err}
	}
	return err
}
