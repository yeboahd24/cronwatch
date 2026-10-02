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

func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "human-readable job name")
	slug := fs.String("slug", "", "stable job slug")
	expr := fs.String("schedule", "", "5-field cron expression")
	grace := fs.Duration("grace", 5*time.Minute, "missed-run grace period")
	dataDir := fs.String("data-dir", "", "data directory")
	noEcho := fs.Bool("no-echo", false, "do not mirror child output")
	maxLogBytes := fs.Int64("max-log-bytes", 1024*1024, "max captured bytes per stream")
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return errors.New("run requires -- command [args...]")
	}
	if err := fs.Parse(args[:sep]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments before --")
	}
	command := args[sep+1:]
	if strings.TrimSpace(*name) == "" {
		return errors.New("--name is required")
	}
	if len(command) == 0 {
		return errors.New("child command is required")
	}
	if *grace < 0 {
		return errors.New("--grace must be non-negative")
	}
	if *maxLogBytes < 0 || *maxLogBytes > 1<<30 {
		return errors.New("--max-log-bytes must be between 0 and 1 GiB")
	}
	if *expr != "" {
		if _, err := schedule.Parse(*expr); err != nil {
			return err
		}
	}
	if *slug == "" {
		*slug = slugify(*name)
	}
	if !validSlug(*slug) {
		return errors.New("--slug must contain lowercase letters, digits, or hyphens")
	}
	if *dataDir == "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		*dataDir = cfg.DataDir
	}
	s, err := storage.Open(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	parts := make([]string, len(command))
	for i, p := range command {
		parts[i] = strconv.Quote(p)
	}
	job, err := s.UpsertJob(ctx, *slug, *name, strings.Join(parts, " "), *expr, *grace)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	run, err := s.CreateRun(ctx, job.ID, started)
	if err != nil {
		return err
	}
	var out, errOut io.Writer
	if !*noEcho {
		out = stdout
		errOut = stderr
	}
	result, runErr := runner.Execute(ctx, command, out, errOut, *maxLogBytes)
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
