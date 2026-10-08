package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/yeboahd24/cronwatch/internal/badge"
	"github.com/yeboahd24/cronwatch/internal/model"
)

func badgeCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("badge", "cronwatch badge [flags] JOB-SLUG | --tag TAG",
		"Print a job's status as an SVG badge, or with --tag how many of the tag's jobs need attention.\n"+
			"Write it to a file that a web server or a wiki publishes, for example from cron:\n\n"+
			"  */5 * * * * cronwatch badge database-backup > /var/www/status/backup.svg\n\n"+
			"cronwatch serve also serves badges at /badge/JOB-SLUG.svg and /badge/tag/TAG.svg.")
	dir := fs.String("data-dir", "", "data directory")
	tag := fs.String("tag", "", "badge for the jobs with this `tag` instead of one job")
	label := fs.String("label", "", "`text` for the badge's left side (default: the job's name or the tag)")
	asJSON := fs.Bool("json", false, "print a shields.io endpoint as JSON instead of SVG")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	// Exactly one of a slug and --tag.
	if fs.NArg() > 1 || (*tag != "") == (fs.NArg() == 1) {
		return errors.New("badge needs one job slug, or --tag TAG")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	now := time.Now()
	var b badge.Badge
	if *tag != "" {
		tags, err := model.Tags([]string{*tag})
		if err != nil || len(tags) != 1 {
			return fmt.Errorf("--tag %q is not one tag", *tag)
		}
		views, err := s.ListJobViews(ctx, now)
		if err != nil {
			return err
		}
		b = badge.ForJobs(tags[0], model.WithTags(views, tags))
	} else {
		job, err := s.GetJobBySlug(ctx, fs.Arg(0))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no job with slug %q", fs.Arg(0))
		}
		if err != nil {
			return err
		}
		view, err := s.JobView(ctx, job, now)
		if err != nil {
			return err
		}
		b = badge.ForJob(view)
	}
	b = b.WithLabel(*label)
	out := b.SVG()
	if *asJSON {
		out = b.ShieldsJSON()
	}
	_, err = fmt.Fprintf(stdout, "%s\n", out)
	return err
}
