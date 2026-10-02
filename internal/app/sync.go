package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/crontab"
	"github.com/yeboahd24/cronwatch/internal/schedule"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// errNoCrontabCommand means the crontab binary is not installed.
var errNoCrontabCommand = errors.New("crontab command not found")

// readUserCrontab returns the current user's crontab; a user without one has
// an empty crontab.
func readUserCrontab(ctx context.Context) (string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "crontab", "-l")
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errNoCrontabCommand
		}
		if strings.Contains(errOut.String(), "no crontab for") {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w: %s", err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// syncResult describes what syncCrontab did.
type syncResult struct {
	Added, Updated []string        // job names
	Unmonitored    []crontab.Entry // lines that do not use cronwatch
	Problems       []string        // lines that use cronwatch run but could not be read
}

// shellEnv is the environment used to expand $VAR in crontab commands: the
// login variables cron provides, overridden by the crontab's assignments.
func shellEnv(assignments map[string]string) map[string]string {
	env := map[string]string{}
	for _, name := range []string{"HOME", "USER", "LOGNAME", "SHELL", "PATH"} {
		if v, ok := os.LookupEnv(name); ok {
			env[name] = v
		}
	}
	for k, v := range assignments {
		env[k] = v
	}
	return env
}

// syncCrontab registers the jobs that crontab lines run through "cronwatch
// run", so they are listed before their first run. New jobs are created with
// their schedule; existing jobs only get their schedule and grace corrected,
// because their name and command are recorded by real runs. Jobs are never
// deleted.
func syncCrontab(ctx context.Context, s *storage.Store, text string) (syncResult, error) {
	var result syncResult
	tab := crontab.Parse(text)
	env := shellEnv(tab.Env)
	seen := map[string]int{} // slug -> line
	for _, entry := range tab.Entries {
		tokens := crontab.Split(entry.Command, env)
		args, found, dynamic := crontab.FindRun(tokens)
		if !found {
			if !crontab.MentionsCronwatch(tokens) {
				result.Unmonitored = append(result.Unmonitored, entry)
			}
			continue
		}
		problem := func(msg string) {
			result.Problems = append(result.Problems, fmt.Sprintf("line %d: %s", entry.Line, msg))
		}
		if dynamic {
			problem("cronwatch run arguments use command substitution")
			continue
		}
		opts, err := parseRunArgs(args, io.Discard)
		if err != nil {
			problem(err.Error())
			continue
		}
		spec := opts.Spec
		if first, dup := seen[spec.Slug]; dup {
			problem(fmt.Sprintf("slug %q is also used on line %d", spec.Slug, first))
			continue
		}
		seen[spec.Slug] = entry.Line
		// Without --schedule, the line's own cron schedule is the schedule.
		if spec.Schedule == nil && entry.Schedule != "" {
			if err := schedule.Validate(entry.Schedule, time.Now()); err != nil {
				problem(err.Error())
				continue
			}
			expr := entry.Schedule
			spec.Schedule = &expr
		}

		existing, err := s.GetJobBySlug(ctx, spec.Slug)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := s.UpsertJob(ctx, spec); err != nil {
				return result, err
			}
			result.Added = append(result.Added, spec.Name)
			continue
		case err != nil:
			return result, err
		}
		update := storage.JobSpec{Slug: existing.Slug, Name: existing.Name, Command: existing.Command}
		if spec.Schedule != nil && (existing.Schedule == nil || *existing.Schedule != *spec.Schedule) {
			update.Schedule = spec.Schedule
		}
		if spec.Grace != nil && int64(*spec.Grace/time.Second) != existing.GraceSeconds {
			update.Grace = spec.Grace
		}
		if update.Schedule == nil && update.Grace == nil {
			continue
		}
		if _, err := s.UpsertJob(ctx, update); err != nil {
			return result, err
		}
		result.Updated = append(result.Updated, existing.Name)
	}
	return result, nil
}

func syncCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("data-dir", "", "data directory")
	file := fs.String("crontab", "", "read this crontab file instead of `crontab -l` (- for stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("sync takes no arguments")
	}
	var text string
	switch *file {
	case "":
		var err error
		if text, err = readUserCrontab(ctx); err != nil {
			return err
		}
	case "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(b)
	default:
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		text = string(b)
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	result, err := syncCrontab(ctx, s, text)
	if err != nil {
		return err
	}
	printSyncResult(stdout, result)
	return nil
}

func printSyncResult(w io.Writer, r syncResult) {
	if len(r.Added) == 0 && len(r.Updated) == 0 {
		fmt.Fprintln(w, "Jobs are up to date with the crontab.")
	}
	for _, name := range r.Added {
		fmt.Fprintf(w, "Added    %s\n", name)
	}
	for _, name := range r.Updated {
		fmt.Fprintf(w, "Updated  %s\n", name)
	}
	if len(r.Problems) > 0 {
		fmt.Fprintf(w, "\nCould not read %d cronwatch line(s):\n", len(r.Problems))
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(r.Unmonitored) > 0 {
		fmt.Fprintf(w, "\nNot monitored (%d line(s) without cronwatch run):\n", len(r.Unmonitored))
		for _, e := range r.Unmonitored {
			fmt.Fprintf(w, "  line %d: %s %s\n", e.Line, e.Raw, e.Command)
		}
	}
}
