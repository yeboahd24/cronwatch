package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// Nagios plugin exit codes.
const (
	checkOK       = 0
	checkWarning  = 1
	checkCritical = 2
	checkUnknown  = 3
)

var checkLabels = [...]string{"OK", "WARNING", "CRITICAL", "UNKNOWN"}

func checkCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("check", "cronwatch check [flags] [JOB-SLUG...]",
		"Print one status line for monitoring systems and exit like a Nagios plugin: 0 OK, 1 WARNING\n"+
			"(a last run was unusually slow, or a job is getting slower), 2 CRITICAL (a job failed, timed\n"+
			"out, missed a run or has an impossible schedule), 3 UNKNOWN. Checks every job unless given slugs.\n"+
			"Paused and archived jobs are OK; archived jobs are left out unless named.")
	dir := fs.String("data-dir", "", "data directory")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	code, lines, err := runCheck(ctx, *dir, fs.Args())
	if err != nil {
		code, lines = checkUnknown, []string{"CRONWATCH UNKNOWN - " + err.Error()}
	}
	fmt.Fprintln(stdout, strings.Join(lines, "\n"))
	if code != checkOK {
		return &ExitError{Code: code, Err: errors.New("check " + strings.ToLower(checkLabels[code]))}
	}
	return nil
}

// runCheck returns the exit code and the output lines: a summary with
// performance data, then one line per job that is not OK.
func runCheck(ctx context.Context, dir string, slugs []string) (int, []string, error) {
	s, err := openForList(ctx, dir)
	if err != nil {
		return 0, nil, err
	}
	defer s.Close()
	now := time.Now()
	views, err := s.ListJobViews(ctx, now)
	if err != nil {
		return 0, nil, err
	}
	if len(slugs) == 0 {
		views = model.Unarchived(views)
	} else {
		bySlug := map[string]model.JobView{}
		for _, v := range views {
			bySlug[v.Slug] = v
		}
		views = views[:0]
		for _, slug := range slugs {
			v, ok := bySlug[slug]
			if !ok {
				return 0, nil, fmt.Errorf("no job with slug %q", slug)
			}
			views = append(views, v)
		}
	}
	code := checkOK
	var counts [3]int
	var problems, details []string
	paused := 0
	for _, v := range views {
		if v.Status == "paused" || v.Status == "archived" {
			paused++ // not monitored, so neither OK nor a problem
			continue
		}
		state, word, detail := checkOK, "", ""
		switch {
		case model.Failing(v.Status) || v.Status == "missed" || v.Status == "invalid_schedule":
			state, word, detail = checkCritical, v.Status, checkDetail(withOutput(ctx, s, v))
		default:
			trend, err := s.JobTrend(ctx, v.ID, 1, now)
			if err != nil {
				return 0, nil, err
			}
			if v.LastRun != nil {
				if sl, ok := trend.Slow[v.LastRun.ID]; ok {
					state, word, detail = checkWarning, "slow", fmt.Sprintf("last run took %.1fx the usual %s", sl.Factor, sl.Usual.Round(time.Second))
				}
			}
			if state == checkOK && trend.Drift != nil {
				state, word, detail = checkWarning, "getting slower", fmt.Sprintf("%s over the last 7 days, up %d%% from %s", trend.Drift.Recent.Round(time.Second), trend.Drift.Percent, trend.Drift.Prior.Round(time.Second))
			}
		}
		counts[state]++
		code = max(code, state)
		if state != checkOK {
			problems = append(problems, v.Name+" "+word)
			details = append(details, fmt.Sprintf("%s: %s: %s", checkLabels[state], v.Name, detail))
		}
	}
	summary := fmt.Sprintf("%s ok", plural(counts[checkOK], "job", "jobs"))
	if len(problems) > 0 {
		summary = strings.Join(problems, ", ")
	}
	if paused > 0 {
		summary += fmt.Sprintf(", %d paused or archived", paused)
	}
	perf := fmt.Sprintf("jobs=%d critical=%d warning=%d ok=%d paused=%d", len(views), counts[checkCritical], counts[checkWarning], counts[checkOK], paused)
	return code, append([]string{fmt.Sprintf("CRONWATCH %s - %s | %s", checkLabels[code], summary, perf)}, details...), nil
}

// checkDetail describes a critical job: its last error or why it is critical.
func checkDetail(v model.JobView) string {
	switch {
	case v.Status == "missed" && v.MissedAt != nil:
		return "missed the run expected " + v.MissedAt.Local().Format("2006-01-02 15:04")
	case v.Status == "invalid_schedule":
		return "the schedule can never run"
	case v.LastRun == nil:
		return v.Status
	}
	detail := v.Status + " " + v.LastRun.StartedAt.Local().Format("2006-01-02 15:04")
	if last := logs.LastError(logs.ParseAs(v.LastRun.Stderr, logs.Stderr)); last != "" {
		detail += ": " + last
	} else if v.LastRun.Reason != "" {
		detail += ": " + v.LastRun.Reason
	}
	return detail
}

// withOutput returns v with its last run's output, which job views leave
// out. If it cannot be read, v is returned as it is.
func withOutput(ctx context.Context, s *storage.Store, v model.JobView) model.JobView {
	if v.LastRun != nil {
		if run, err := s.GetRun(ctx, v.LastRun.ID); err == nil {
			v.LastRun = &run
		}
	}
	return v
}
