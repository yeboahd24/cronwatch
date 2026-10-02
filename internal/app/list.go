package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
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
	return storage.Open(ctx, dir)
}

func jobsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("jobs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("jobs takes no arguments")
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
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tLAST RUN\tDURATION")
	for _, v := range views {
		last, duration := "—", "—"
		if v.LastRun != nil {
			last = v.LastRun.StartedAt.Local().Format("2006-01-02 15:04")
			if v.LastRun.DurationMS != nil {
				duration = (time.Duration(*v.LastRun.DurationMS) * time.Millisecond).String()
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", v.Name, v.Status, last, duration)
	}
	return w.Flush()
}

func runsCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("runs accepts at most one job slug")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	var runs []struct {
		ID, Name, Status string
		StartedAt        time.Time
	}
	if fs.NArg() == 1 {
		job, err := s.GetJobBySlug(ctx, fs.Arg(0))
		if err != nil {
			return err
		}
		items, err := s.ListRunsForJob(ctx, job.ID, 100)
		if err != nil {
			return err
		}
		for _, r := range items {
			runs = append(runs, struct {
				ID, Name, Status string
				StartedAt        time.Time
			}{r.ID, job.Name, r.Status, r.StartedAt})
		}
	} else {
		items, err := s.ListRuns(ctx, 100)
		if err != nil {
			return err
		}
		for _, r := range items {
			job, err := s.GetJob(ctx, r.JobID)
			if err != nil {
				return err
			}
			runs = append(runs, struct {
				ID, Name, Status string
				StartedAt        time.Time
			}{r.ID, job.Name, r.Status, r.StartedAt})
		}
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN ID\tJOB\tSTATUS\tSTARTED")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Status, r.StartedAt.Local().Format("2006-01-02 15:04:05"))
	}
	return w.Flush()
}
