package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
}

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
	if *slug == "" {
		*slug = slugify(*name)
	}
	if !validSlug(*slug) {
		return opts, errors.New("--slug must contain lowercase letters, digits, or hyphens")
	}
	parts := make([]string, len(command))
	for i, p := range command {
		parts[i] = strconv.Quote(p)
	}
	opts = runOptions{
		Spec:        storage.JobSpec{Slug: *slug, Name: *name, Command: strings.Join(parts, " ")},
		Command:     command,
		DataDir:     *dataDir,
		NoEcho:      *noEcho,
		MaxLogBytes: *maxLogBytes,
	}
	// Only flags passed on this invocation change the stored job.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "schedule":
			opts.Spec.Schedule = expr
		case "grace":
			opts.Spec.Grace = grace
		}
	})
	return opts, nil
}

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseRunArgs(args, stdout)
	if err != nil {
		return err
	}
	spec, command := opts.Spec, opts.Command
	if opts.DataDir == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		opts.DataDir = cfg.DataDir
	}
	s, err := storage.Open(ctx, opts.DataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	// Different names that slugify alike would silently share one history.
	if existing, err := s.GetJobBySlug(ctx, spec.Slug); err == nil && existing.Name != spec.Name {
		fmt.Fprintf(stderr, "cronwatch: warning: slug %q belongs to job %q; renaming it to %q (pass --slug to keep them separate)\n", spec.Slug, existing.Name, spec.Name)
	}
	job, err := s.UpsertJob(ctx, spec)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	run, err := s.CreateRun(ctx, job.ID, started)
	if err != nil {
		return err
	}
	var out, errOut io.Writer
	if !opts.NoEcho {
		out = stdout
		errOut = stderr
	}
	result, runErr := runner.Execute(ctx, command, out, errOut, opts.MaxLogBytes)
	code := result.ExitCode
	// A cancelled context cannot be used to save the final state.
	finishCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.FinishRun(finishCtx, run.ID, time.Now().UTC(), result.Duration, result.Status, &code, result.Stdout, result.Stderr, result.Combined, result.Truncated); err != nil {
		return fmt.Errorf("record run result: %w", err)
	}
	if runErr != nil {
		return &ExitError{Code: code, Err: runErr}
	}
	return nil
}
