package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
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
	Jobs           int                     // cronwatch run lines registered (new, updated or unchanged)
	Added, Updated []string                // job names
	Unmonitored    []crontab.Entry         // lines that do not use cronwatch
	Problems       []string                // lines that use cronwatch run but could not be read
	Changes        []storage.CrontabChange // since the last recorded crontab
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
	maps.Copy(env, assignments)
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
		// The crontab is the source of truth for its lines' hooks: a hook
		// variable removed from it is removed from the job.
		for _, h := range []struct {
			dst **string
			env string
		}{{&spec.OnFailure, envOnFailure}, {&spec.OnRecover, envOnRecover}} {
			if *h.dst == nil {
				v := tab.Env[h.env]
				*h.dst = &v
			}
		}
		// Without --schedule, the line's own cron schedule is the schedule.
		if spec.Schedule == nil && entry.Schedule != "" {
			if err := schedule.Validate(entry.Schedule, time.Now()); err != nil {
				problem(err.Error())
				continue
			}
			expr := entry.Schedule
			spec.Schedule = &expr
		}
		result.Jobs++

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
		if *spec.OnFailure != existing.OnFailure {
			update.OnFailure = spec.OnFailure
		}
		if *spec.OnRecover != existing.OnRecover {
			update.OnRecover = spec.OnRecover
		}
		if update.Schedule == nil && update.Grace == nil && update.OnFailure == nil && update.OnRecover == nil {
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
	fs := newFlagSet("sync", "cronwatch sync [--crontab FILE]",
		"Register the jobs your crontab runs through \"cronwatch run\", and list lines that are not monitored.")
	dir := fs.String("data-dir", "", "data directory")
	file := fs.String("crontab", "", "read this crontab `file` instead of \"crontab -l\" (- for stdin)")
	if err := parseFlags(fs, args, stdout); err != nil {
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
	// Only your real crontab has a history; a file may be a draft.
	if *file == "" {
		if result.Changes, err = recordCrontab(ctx, s, text, time.Now()); err != nil {
			return err
		}
	}
	printSyncResult(stdout, result)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func printSyncResult(w io.Writer, r syncResult) {
	for _, name := range r.Added {
		fmt.Fprintf(w, "Added    %s\n", name)
	}
	for _, name := range r.Updated {
		fmt.Fprintf(w, "Updated  %s\n", name)
	}
	if len(r.Added)+len(r.Updated) > 0 {
		fmt.Fprintln(w)
	}

	total := r.Jobs + len(r.Problems)
	var summary string
	switch {
	case total == 0:
		summary = "No crontab lines use cronwatch run."
	case len(r.Problems) == 0:
		summary = plural(total, "job", "jobs") + " in the crontab, all registered."
	default:
		summary = fmt.Sprintf("%d of %s in the crontab registered.", r.Jobs, plural(total, "job", "jobs"))
	}
	if len(r.Unmonitored) == 0 && total > 0 {
		summary += " Every crontab line is monitored."
	}
	fmt.Fprintln(w, summary)

	if len(r.Problems) > 0 {
		fmt.Fprintf(w, "\nCould not read %s:\n", plural(len(r.Problems), "cronwatch line", "cronwatch lines"))
		for _, p := range r.Problems {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(r.Unmonitored) > 0 {
		fmt.Fprintf(w, "\nNot monitored (%s without cronwatch run):\n", plural(len(r.Unmonitored), "line", "lines"))
		for _, e := range r.Unmonitored {
			fmt.Fprintf(w, "  line %d: %s %s\n", e.Line, e.Raw, e.Command)
		}
	}
	if len(r.Changes) > 0 {
		fmt.Fprintf(w, "\nChanged since the crontab was last recorded:\n")
		for _, c := range r.Changes {
			fmt.Fprintf(w, "  %s\n", describeChange(c, nil))
		}
	}
}
