package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/runner"
	"github.com/yeboahd24/cronwatch/internal/schedule"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

const maxLogBytesLimit = 64 << 20

type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

func slugify(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if unicode.IsSpace(r) || r == '-' || r == '_' {
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func validSlug(slug string) bool {
	if slug == "" || slug[0] == '-' || slug[len(slug)-1] == '-' {
		return false
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// runOptions is a parsed "cronwatch run" invocation.
type runOptions struct {
	Spec        storage.JobSpec // only flags that were passed are set
	Command     []string
	DataDir     string
	NoEcho      bool
	MaxLogBytes int64
	Rules       runRules
	StrictExit  bool
	NoOverlap   bool
	// OnStorageError is "run" to run the command even when the run cannot
	// be recorded, "fail" not to, or "" when not passed.
	OnStorageError string
}

// validStorageAction checks a --on-storage-error value from source.
func validStorageAction(source, v string) error {
	switch v {
	case "", "fail", "run":
		return nil
	}
	return fmt.Errorf("%s must be fail or run, not %q", source, v)
}

// envOnStorageError sets --on-storage-error when it is not passed.
const envOnStorageError = "CRONWATCH_ON_STORAGE_ERROR"

// parseRunArgs parses "cronwatch run" arguments. It is shared with crontab
// sync so both derive the same job from the same arguments.
// help receives --help output.
func parseRunArgs(args []string, help io.Writer) (runOptions, error) {
	var opts runOptions
	fs := newFlagSet("run", "cronwatch run --name NAME [flags] -- command [args...]",
		"Run a command, pass its output through, and record the result. Exits with the command's exit code.")
	name := fs.String("name", "", "job `name` shown on the dashboard (required)")
	slug := fs.String("slug", "", "stable job `id` (default: the name in lowercase with dashes)")
	expr := fs.String("schedule", "", "five-field cron `expression` CronWatch should expect the job on")
	grace := fs.Duration("grace", storage.DefaultGrace, "how late a run may start before it counts as missed")
	dataDir := fs.String("data-dir", "", "data directory")
	noEcho := fs.Bool("no-echo", false, "record output without also printing it")
	maxLogBytes := fs.Int64("max-log-bytes", 1024*1024, "output `bytes` kept per stream (max 64 MiB)")
	okCodes := fs.String("ok-codes", "", "comma-separated exit `codes` besides 0 that count as success, e.g. 3,4")
	failOnStderr := fs.Bool("fail-on-stderr", false, "mark the run failed if the command writes anything to stderr")
	failMatch := fs.String("fail-if-match", "", "mark the run failed if its output matches this `regexp`")
	successMatch := fs.String("success-if-match", "", "mark the run failed unless its output matches this `regexp`")
	timeout := fs.Duration("timeout", 0, "stop the command after this `duration` (SIGTERM, then SIGKILL 5s later) and record a timeout")
	strictExit := fs.Bool("strict-exit", false, "exit 0 for a successful run and nonzero for a failed one, even when the rules above disagree with the command's exit code")
	noOverlap := fs.Bool("no-overlap", false, "skip this run, and record it as skipped, if the job's previous run is still running")
	onFailure := fs.String("on-failure", "", "shell `command` to run when the job starts failing, times out or misses a run (default $"+envOnFailure+")")
	onRecover := fs.String("on-recover", "", "shell `command` to run when the job succeeds again after failing (default $"+envOnRecover+")")
	onStorageError := fs.String("on-storage-error", "", "`action` when the run cannot be recorded: fail (do not run the command) or run (run it unrecorded) (default $"+envOnStorageError+", else fail)")
	sep := len(args)
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if err := parseFlags(fs, args[:sep], help); err != nil {
		return opts, err
	}
	if sep == len(args) {
		return opts, errors.New("run requires -- command [args...]")
	}
	if fs.NArg() != 0 {
		return opts, errors.New("unexpected arguments before --")
	}
	command := args[sep+1:]
	if strings.TrimSpace(*name) == "" {
		return opts, errors.New("--name is required")
	}
	if len(command) == 0 {
		return opts, errors.New("child command is required")
	}
	if *grace < 0 {
		return opts, errors.New("--grace must be non-negative")
	}
	// Logs are buffered in memory and the combined log (2x) must fit in one
	// SQLite value, so keep the cap well below SQLite's 1 GB limit.
	if *maxLogBytes < 0 || *maxLogBytes > maxLogBytesLimit {
		return opts, errors.New("--max-log-bytes must be between 0 and 64 MiB")
	}
	if *expr != "" {
		if err := schedule.Validate(*expr, time.Now()); err != nil {
			return opts, err
		}
	}
	if *timeout < 0 {
		return opts, errors.New("--timeout must be non-negative")
	}
	rules := runRules{FailOnStderr: *failOnStderr, Timeout: *timeout}
	if *okCodes != "" {
		codes, err := parseOKCodes(*okCodes)
		if err != nil {
			return opts, err
		}
		rules.OKCodes = codes
	}
	for _, m := range []struct {
		flag, expr string
		dst        **regexp.Regexp
	}{{"--fail-if-match", *failMatch, &rules.FailMatch}, {"--success-if-match", *successMatch, &rules.SuccessMatch}} {
		if m.expr == "" {
			continue
		}
		re, err := regexp.Compile(m.expr)
		if err != nil {
			return opts, fmt.Errorf("%s: %w", m.flag, err)
		}
		*m.dst = re
	}
	if err := validStorageAction("--on-storage-error", *onStorageError); err != nil {
		return opts, err
	}
	if *slug == "" {
		*slug = slugify(*name)
	}
	if !validSlug(*slug) {
		return opts, errors.New("--slug must contain lowercase letters, digits, or hyphens")
	}
	opts = runOptions{
		Spec:           storage.JobSpec{Slug: *slug, Name: *name, Command: joinCommand(command)},
		Command:        command,
		DataDir:        *dataDir,
		NoEcho:         *noEcho,
		MaxLogBytes:    *maxLogBytes,
		Rules:          rules,
		StrictExit:     *strictExit,
		NoOverlap:      *noOverlap,
		OnStorageError: *onStorageError,
	}
	// Only flags passed on this invocation change the stored job.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "schedule":
			opts.Spec.Schedule = expr
		case "grace":
			opts.Spec.Grace = grace
		case "on-failure":
			opts.Spec.OnFailure = onFailure
		case "on-recover":
			opts.Spec.OnRecover = onRecover
		}
	})
	return opts, nil
}

// joinCommand stores argv as Go-quoted words; splitCommand reverses it.
func joinCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, p := range argv {
		parts[i] = strconv.Quote(p)
	}
	return strings.Join(parts, " ")
}

func splitCommand(command string) ([]string, error) {
	var argv []string
	for rest := strings.TrimSpace(command); rest != ""; rest = strings.TrimLeft(rest, " ") {
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return nil, fmt.Errorf("stored command %q is not in the expected format", command)
		}
		word, _ := strconv.Unquote(quoted)
		argv = append(argv, word)
		rest = rest[len(quoted):]
	}
	if len(argv) == 0 {
		return nil, errors.New("stored command is empty")
	}
	return argv, nil
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseRunArgs(args, stdout)
	if err != nil {
		return err
	}
	// Variables set in the crontab apply when the flags are not passed.
	if opts.OnStorageError == "" {
		opts.OnStorageError = os.Getenv(envOnStorageError)
		if err := validStorageAction(envOnStorageError, opts.OnStorageError); err != nil {
			return err
		}
	}
	spec := opts.Spec
	for _, h := range []struct {
		dst **string
		env string
	}{{&spec.OnFailure, envOnFailure}, {&spec.OnRecover, envOnRecover}} {
		if v, ok := os.LookupEnv(h.env); ok && *h.dst == nil {
			*h.dst = &v
		}
	}
	if opts.DataDir == "" {
		cfg, err := config.Load()
		if err != nil {
			return runUnrecorded(ctx, opts, nil, false, err, stdout, stderr)
		}
		opts.DataDir = cfg.DataDir
	}
	// The job lock is held while the command runs, so a second run of the
	// same job can tell that the first has not finished. It is taken before
	// the database is opened, so it works even when the database does not.
	lock, locked, err := lockJob(opts.DataDir, spec.Slug)
	if err != nil {
		return runUnrecorded(ctx, opts, nil, false, err, stdout, stderr)
	}
	defer lock.Close()
	s, err := storage.Open(ctx, opts.DataDir)
	if err != nil {
		return runUnrecorded(ctx, opts, lock, locked, err, stdout, stderr)
	}
	defer s.Close()
	// Different names that slugify alike would silently share one history.
	if existing, err := s.GetJobBySlug(ctx, spec.Slug); err == nil && existing.Name != spec.Name {
		fmt.Fprintf(stderr, "cronwatch: warning: slug %q belongs to job %q; renaming it to %q (pass --slug to keep them separate)\n", spec.Slug, existing.Name, spec.Name)
	}
	job, err := s.UpsertJob(ctx, spec)
	if err != nil {
		return runUnrecorded(ctx, opts, lock, locked, err, stdout, stderr)
	}
	runs := lifecycle{s: s, stderr: stderr}
	run, err := runs.start(ctx, job, time.Now().UTC(), false)
	if err != nil {
		return runUnrecorded(ctx, opts, lock, locked, err, stdout, stderr)
	}
	if !locked {
		previous := ""
		if prev, err := s.OtherRunningRun(ctx, job.ID, run.ID); err == nil {
			previous = prev.ID
		}
		if opts.NoOverlap {
			return skipRun(ctx, runs, job, run, previous)
		}
		if err := s.SetRunOverlap(ctx, run.ID, previous); err != nil {
			fmt.Fprintf(stderr, "cronwatch: warning: could not record the overlap: %v\n", err)
		}
	}
	result, status, reason, runErr := execute(ctx, opts, saveOutput(s, run.ID, stderr), stdout, stderr)
	code := result.ExitCode
	// A cancelled context cannot be used to save the final state.
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := runs.finish(finishCtx, job, run.ID, storage.Completion{Ended: time.Now().UTC(), Duration: result.Duration,
		Status: status, ExitCode: &code, Stdout: result.Stdout, Stderr: result.Stderr, Combined: result.Combined,
		Truncated: result.Truncated, Reason: reason, Usage: result.Usage}); err != nil {
		if opts.OnStorageError != "run" {
			return fmt.Errorf("record run result: %w", err)
		}
		// The command has run; keep its exit code rather than report ours.
		fmt.Fprintf(stderr, "cronwatch: warning: could not record the run's result (--on-storage-error run): %v\n", err)
	}
	return finalExit(status, code, runErr, opts.StrictExit)
}

// How often a running command's output is saved, while it changes: every
// liveEvery, or every liveSlowEvery once it is over liveLarge bytes, so a
// job with a lot of output does not keep rewriting megabytes. Tests shorten
// them.
var (
	liveEvery     = 5 * time.Second
	liveSlowEvery = 30 * time.Second
	liveLarge     = 1 << 20
)

// saveOutput returns a runner progress function that saves the output so far
// of the run with id. Saving is best effort: a failure is reported once and
// does not affect the run.
func saveOutput(s *storage.Store, id string, stderr io.Writer) func(runner.Output) {
	warned := false
	return func(out runner.Output) {
		ctx, cancel := context.WithTimeout(context.Background(), liveEvery)
		defer cancel()
		if err := s.SaveRunOutput(ctx, id, out, time.Now()); err != nil && !warned {
			warned = true
			fmt.Fprintf(stderr, "cronwatch: warning: could not save the output so far: %v\n", err)
		}
	}
}

// execute runs the command with opts' output, timeout and rules, and judges
// the result. save, if not nil, receives the output so far while the command
// runs.
func execute(ctx context.Context, opts runOptions, save func(runner.Output), stdout, stderr io.Writer) (result runner.Result, status, reason string, runErr error) {
	var out, errOut io.Writer
	if !opts.NoEcho {
		out = stdout
		errOut = stderr
	}
	runCtx := ctx
	if opts.Rules.Timeout > 0 {
		var cancelRun context.CancelFunc
		runCtx, cancelRun = context.WithTimeout(ctx, opts.Rules.Timeout)
		defer cancelRun()
	}
	var seen outputSeen
	result, runErr = runner.Execute(runCtx, opts.Command, runner.Options{Stdout: out, Stderr: errOut,
		MaxLogBytes: opts.MaxLogBytes, OnLine: opts.Rules.watch(&seen),
		Progress: save, ProgressEvery: liveEvery, ProgressAfter: liveSlowEvery, ProgressLarge: liveLarge})
	status, reason = opts.Rules.judge(result, seen)
	if reason != "" && status != "success" {
		fmt.Fprintf(stderr, "cronwatch: %s: %s\n", status, reason)
	}
	return result, status, reason, runErr
}

// finalExit is what "cronwatch run" returns once the command has run.
func finalExit(status string, code int, runErr error, strict bool) error {
	if exit := exitCode(status, code, strict); exit != 0 {
		if runErr == nil {
			runErr = errRuleFailed
		}
		return &ExitError{Code: exit, Err: runErr}
	}
	return nil
}

// runUnrecorded handles a run that cannot be recorded because of storeErr.
// By default the command does not run and storeErr is returned. With
// --on-storage-error run, the command runs as usual but nothing is stored
// and no hooks run, since whether the job changed state is not known.
// --no-overlap still holds: the run is skipped if the job's lock is held by
// another run, and the command does not run at all if the lock could not be
// checked (lock is nil).
func runUnrecorded(ctx context.Context, opts runOptions, lock *os.File, locked bool, storeErr error, stdout, stderr io.Writer) error {
	if opts.OnStorageError != "run" {
		return storeErr
	}
	if opts.NoOverlap && lock == nil {
		return fmt.Errorf("not running: --no-overlap cannot check for another run: %w", storeErr)
	}
	fmt.Fprintf(stderr, "cronwatch: warning: this run will not be recorded (--on-storage-error run): %v\n", storeErr)
	if opts.NoOverlap && !locked {
		fmt.Fprintln(stderr, "cronwatch: skipped: another run of this job was still running (--no-overlap)")
		return nil
	}
	result, status, _, runErr := execute(ctx, opts, nil, stdout, stderr)
	return finalExit(status, result.ExitCode, runErr, opts.StrictExit)
}
