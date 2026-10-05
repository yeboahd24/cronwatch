package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func digestCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("digest", "cronwatch digest [--since DURATION] [--quiet]",
		"Print a plain-text summary of every job: what needs attention now, and runs, failures and\n"+
			"missed runs in the period. Run it from cron and cron's MAILTO emails it to you.")
	dir := fs.String("data-dir", "", "data directory")
	since := fs.Duration("since", 24*time.Hour, "length of the period to summarize")
	quiet := fs.Bool("quiet", false, "print nothing when no job failed, timed out or missed a run in the period and none is failing now")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("digest takes no arguments")
	}
	if *since <= 0 {
		return errors.New("--since must be positive")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	now := time.Now()
	views, err := s.ListJobViews(ctx, now)
	if err != nil {
		return err
	}
	activity, err := s.ActivitySince(ctx, now.Add(-*since))
	if err != nil {
		return err
	}
	d := buildDigest(views, activity)
	if *quiet && !d.problems {
		return nil
	}
	host, _ := os.Hostname()
	d.print(stdout, host, now.Add(-*since), now)
	return nil
}

type digestRow struct {
	View                 model.JobView
	Runs, Failed, Missed int64
}

type digest struct {
	rows      []digestRow
	attention []digestRow // failing or missing now
	problems  bool        // anything failed or was missed in the period, or is failing now
}

func buildDigest(views []model.JobView, activity map[string]*storage.JobActivity) digest {
	var d digest
	for _, v := range views {
		row := digestRow{View: v}
		if a := activity[v.ID]; a != nil {
			for status, n := range a.Runs {
				row.Runs += n
				if model.Failing(status) {
					row.Failed += n
				}
			}
			row.Missed = a.Missed
		}
		d.rows = append(d.rows, row)
		if model.Failing(v.Status) || v.Status == "missed" || v.Status == "invalid_schedule" {
			d.attention = append(d.attention, row)
		}
		if row.Failed > 0 || row.Missed > 0 {
			d.problems = true
		}
	}
	if len(d.attention) > 0 {
		d.problems = true
	}
	return d
}

func (d digest) print(w io.Writer, host string, from, to time.Time) {
	format := func(t time.Time) string { return t.Local().Format("Jan 2 15:04") }
	fmt.Fprintf(w, "CronWatch digest for %s, %s to %s\n\n", host, format(from), format(to))
	if len(d.rows) == 0 {
		fmt.Fprintln(w, "No jobs are registered.")
		return
	}
	if len(d.attention) == 0 {
		fmt.Fprintln(w, "Nothing needs attention: no job is failing or missing runs.")
	} else {
		fmt.Fprintf(w, "Needs attention (%d)\n", len(d.attention))
		for _, r := range d.attention {
			fmt.Fprintf(w, "  %s: %s\n", r.View.Name, attentionLine(r.View))
		}
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "JOB\tSTATUS\tRUNS\tFAILED\tMISSED\tLAST RUN")
	for _, r := range d.rows {
		last := "—"
		if r.View.LastRun != nil {
			last = format(r.View.LastRun.StartedAt)
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", r.View.Name, r.View.Status, r.Runs, r.Failed, r.Missed, last)
	}
	tw.Flush()
}

// attentionLine says why a job needs attention, with the error if there is one.
func attentionLine(v model.JobView) string {
	switch {
	case v.Status == "missed" && v.MissedAt != nil:
		return "missed the run expected " + v.MissedAt.Local().Format("Jan 2 15:04")
	case v.Status == "invalid_schedule":
		return "its schedule can never run"
	case v.LastRun == nil:
		return v.Status
	}
	r := v.LastRun
	line := fmt.Sprintf("%s %s", r.Status, r.StartedAt.Local().Format("Jan 2 15:04"))
	switch {
	case r.Reason != "":
		line += ": " + r.Reason
	case r.ExitCode != nil:
		line += fmt.Sprintf(" (exit %d)", *r.ExitCode)
	}
	if last := logs.LastError(logs.ParseAs(r.Stderr, logs.Stderr)); last != "" && r.Reason == "" {
		line += ": " + last
	}
	return line
}
