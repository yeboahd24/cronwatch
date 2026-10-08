package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func openForList(ctx context.Context, dir string) (*storage.Store, error) {
	if dir == "" {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		dir = cfg.DataDir
	}
	s, err := storage.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	if err := maintain(ctx, s, os.Stderr); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func jobsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("jobs", "cronwatch jobs [flags]", "List every job with its current status. Archived jobs are listed with --all.")
	dir := fs.String("data-dir", "", "data directory")
	all := fs.Bool("all", false, "include archived jobs")
	asJSON := fs.Bool("json", false, "print a JSON array instead of a table")
	var tagFlags stringList
	fs.Var(&tagFlags, "tag", "only jobs with this `tag`; repeat for jobs with all of them")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("jobs takes no arguments")
	}
	tags, err := model.Tags(tagFlags)
	if err != nil {
		return fmt.Errorf("--%w", err)
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	views, err := s.ListJobViews(ctx, time.Now())
	if err != nil {
		return err
	}
	if !*all {
		views = model.Unarchived(views)
	}
	views = model.WithTags(views, tags)
	if *asJSON {
		out := make([]jsonJob, 0, len(views))
		for _, v := range views {
			out = append(out, newJSONJob(v))
		}
		return writeJSON(stdout, out)
	}
	// A TAGS column only once some job has tags, so the table stays as it
	// was for those who use none.
	showTags := slices.ContainsFunc(views, func(v model.JobView) bool { return len(v.Tags) > 0 })
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	header := "NAME\tSTATUS\tLAST RUN\tDURATION"
	if showTags {
		header += "\tTAGS"
	}
	fmt.Fprintln(w, header)
	for _, v := range views {
		last, duration := "—", "—"
		if v.LastRun != nil {
			last = v.LastRun.StartedAt.Local().Format("2006-01-02 15:04")
			if v.LastRun.DurationMS != nil {
				duration = (time.Duration(*v.LastRun.DurationMS) * time.Millisecond).String()
			}
		}
		status := v.Status
		if status == "paused" && v.PausedUntil != nil {
			status += " until " + v.PausedUntil.Local().Format("2006-01-02 15:04")
		}
		line := fmt.Sprintf("%s\t%s\t%s\t%s", v.Name, status, last, duration)
		if showTags {
			line += "\t" + strings.Join(v.Tags, ",")
		}
		fmt.Fprintln(w, line)
	}
	return w.Flush()
}

func runsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("runs", "cronwatch runs [flags] [job-slug]", "List the most recent runs, newest first, optionally for one job.")
	dir := fs.String("data-dir", "", "data directory")
	asJSON := fs.Bool("json", false, "print a JSON array instead of a table")
	status := fs.String("status", "", "only runs with this `status`: "+strings.Join(model.RunStatuses, ", "))
	since := fs.Duration("since", 0, "only runs started within this `duration`, e.g. 24h")
	limit := fs.Int("limit", 100, "list at most this many `runs` (0 for all)")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("runs accepts at most one job slug")
	}
	if *status != "" && !slices.Contains(model.RunStatuses, *status) {
		return fmt.Errorf("--status must be one of %s", strings.Join(model.RunStatuses, ", "))
	}
	if *since < 0 || *limit < 0 {
		return errors.New("--since and --limit must not be negative")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		return err
	}
	byID := map[string]model.Job{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	filter := storage.RunFilter{Status: *status}
	if *since > 0 {
		filter.Since = time.Now().Add(-*since)
	}
	if fs.NArg() == 1 {
		job, err := s.GetJobBySlug(ctx, fs.Arg(0))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no job with slug %q", fs.Arg(0))
		}
		if err != nil {
			return err
		}
		filter.JobID = job.ID
	}
	rows := *limit
	if rows == 0 {
		rows = -1 // SQLite: no limit
	}
	items, err := s.ListRunSummaries(ctx, filter, nil, rows)
	if err != nil {
		return err
	}
	runs := make([]model.Run, 0, len(items))
	for _, r := range items {
		runs = append(runs, r.Run)
	}
	if *asJSON {
		out := make([]jsonRun, 0, len(runs))
		for _, r := range runs {
			out = append(out, newJSONRun(r, byID[r.JobID]))
		}
		return writeJSON(stdout, out)
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN ID\tJOB\tSTATUS\tSTARTED")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, byID[r.JobID].Name, r.Status, r.StartedAt.Local().Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}

func pruneCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("prune", "cronwatch prune [--keep N] [--older-than DURATION]",
		"Delete finished runs beyond the newest N per job and/or older than a duration. Running runs are kept.")
	dir := fs.String("data-dir", "", "data directory")
	keep := fs.Int("keep", 0, "finished runs to keep per job (0 = no limit)")
	olderThan := fs.Duration("older-than", 0, "delete runs, missed occurrences, crontab history and finished alerts older than this, e.g. 720h (0 = no limit)")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("prune takes no arguments")
	}
	if *keep < 0 || *olderThan < 0 {
		return errors.New("--keep and --older-than must be non-negative")
	}
	if *keep == 0 && *olderThan == 0 {
		return errors.New("prune requires --keep or --older-than")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	var before time.Time
	if *olderThan > 0 {
		before = time.Now().Add(-*olderThan)
	}
	result, err := s.Prune(ctx, *keep, before)
	if err != nil {
		return err
	}
	parts := []string{plural(int(result.Runs), "run", "runs"), plural(int(result.MissedOccurrences), "missed occurrence", "missed occurrences")}
	if result.CrontabSnapshots > 0 {
		parts = append(parts, plural(int(result.CrontabSnapshots), "crontab snapshot", "crontab snapshots"))
	}
	if result.Alerts > 0 {
		parts = append(parts, plural(int(result.Alerts), "alert", "alerts"))
	}
	fmt.Fprint(stdout, "Deleted "+strings.Join(parts[:len(parts)-1], ", ")+" and "+parts[len(parts)-1])
	fmt.Fprintln(stdout, ".")
	return nil
}
