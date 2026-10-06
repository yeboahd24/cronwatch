package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
)

// jobStateCommand runs "pause", "resume" or "archive" for each slug given.
func jobStateCommand(ctx context.Context, name string, args []string, stdout io.Writer) error {
	usage := map[string][2]string{
		"pause": {"cronwatch pause [--for DURATION] JOB-SLUG...",
			"Stop checking jobs for missed runs and raising their alerts, until resumed or for a while.\n" +
				"Their runs are still recorded."},
		"resume":  {"cronwatch resume JOB-SLUG...", "Monitor paused or archived jobs again. Missed runs are checked from now."},
		"archive": {"cronwatch archive JOB-SLUG...", "Stop monitoring jobs and leave them off job lists, keeping their runs.\nresume brings them back."},
	}[name]
	fs := newFlagSet(name, usage[0], usage[1])
	dir := fs.String("data-dir", "", "data directory")
	var pauseFor *time.Duration
	if name == "pause" {
		pauseFor = fs.Duration("for", 0, "resume by itself after this `duration`, e.g. 2h (default: stay paused until resumed)")
	}
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("%s requires at least one job slug", name)
	}
	if pauseFor != nil && *pauseFor < 0 {
		return errors.New("--for must be positive")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	// Every slug is looked up first, so a typo changes nothing.
	var jobs []model.Job
	for _, slug := range fs.Args() {
		job, err := s.GetJobBySlug(ctx, slug)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no job with slug %q", slug)
		}
		if err != nil {
			return err
		}
		jobs = append(jobs, job)
	}
	now := time.Now()
	for _, job := range jobs {
		switch name {
		case "pause":
			var until *time.Time
			if *pauseFor > 0 {
				t := now.Add(*pauseFor)
				until = &t
			}
			if err := s.PauseJob(ctx, job, now, until); err != nil {
				return err
			}
			if until != nil {
				fmt.Fprintf(stdout, "Paused %s until %s.\n", job.Name, until.Local().Format("2006-01-02 15:04"))
			} else {
				fmt.Fprintf(stdout, "Paused %s until you resume it.\n", job.Name)
			}
		case "archive":
			if job.ArchivedAt != nil {
				fmt.Fprintf(stdout, "%s is already archived.\n", job.Name)
				continue
			}
			if err := s.ArchiveJob(ctx, job, now); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Archived %s. Its runs are kept; resume it to monitor it again.\n", job.Name)
		case "resume":
			resumed, err := s.ResumeJob(ctx, job, now)
			if err != nil {
				return err
			}
			if resumed {
				fmt.Fprintf(stdout, "Resumed %s.\n", job.Name)
			} else {
				fmt.Fprintf(stdout, "%s is not paused or archived.\n", job.Name)
			}
		}
	}
	return nil
}
