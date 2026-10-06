package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/schedule"
)

// timelineRanges are the windows the Timeline page offers, newest at the right.
var timelineRanges = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour}

const (
	// maxExpectedTicks hides a lane's expected-run ticks when they would be so
	// dense that they read as a solid band; the runs show the cadence instead.
	maxExpectedTicks = 200
	// minMarkPct keeps very short runs visible and hoverable; missed runs are
	// wider so their dashed outline reads as a box.
	minMarkPct   = 0.25
	minMissedPct = 0.6
	// nowGapPct keeps axis labels clear of the "now" label at the right edge.
	nowGapPct = 94
)

type timeline struct {
	Range string
	Ticks []timelineTick
	Lanes []timelineLane
}

type timelineTick struct {
	Pct   string // position as an SVG percentage, e.g. "12.5%"
	Label string
}

type timelineLane struct {
	Job      model.JobView
	Expected []string // positions of expected runs
	Changes  []timelineChange
	Marks    []timelineMark
	Summary  string // for screen readers
}

// timelineMark is a run or a missed occurrence. Kind sets both its color and
// its shape, so status never relies on color alone: failures are full-height
// bars, successes half-height, missed runs dashed outlines, and skipped or
// cancelled runs thin ticks.
type timelineMark struct {
	Kind     string // ok, fail, running, minor or missed
	X, W     string
	Title    string
	Href     string
	Overlaps bool // started while an earlier run was still running
}

// timelineChange marks when the job's crontab line changed.
type timelineChange struct {
	X     string
	Title string
}

func markKind(status string) string {
	switch {
	case status == "success":
		return "ok"
	case model.Failing(status):
		return "fail"
	case status == "running":
		return "running"
	default:
		return "minor"
	}
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("range")
	window, ok := timelineRanges[key]
	if !ok {
		key, window = "24h", timelineRanges["24h"]
	}
	to := s.now()
	from := to.Add(-window)
	jobs, err := s.Store.ListJobViews(r.Context(), to)
	if err != nil {
		queryError(w, err)
		return
	}
	jobs = model.Unarchived(jobs)
	runs, err := s.Store.RunsOverlapping(r.Context(), from, to)
	if err != nil {
		queryError(w, err)
		return
	}
	missed, err := s.Store.MissedSince(r.Context(), from)
	if err != nil {
		queryError(w, err)
		return
	}
	crontabChanges, err := s.Store.JobCrontabChangesSince(r.Context(), from)
	if err != nil {
		queryError(w, err)
		return
	}

	pct := func(t time.Time) float64 {
		p := float64(t.Sub(from)) / float64(window) * 100
		return min(max(p, 0), 100)
	}
	format := func(p float64) string { return fmt.Sprintf("%.3f%%", p) }
	span := func(start, end time.Time, minWidth float64) (x, w string) {
		left := pct(start)
		width := max(pct(end)-left, minWidth)
		left = min(left, 100-width)
		return format(left), format(width)
	}

	byJob := map[string][]model.Run{}
	for _, run := range runs {
		byJob[run.JobID] = append(byJob[run.JobID], run)
	}
	tl := timeline{Range: key, Ticks: timelineTicks(from, to, window)}
	for _, job := range jobs {
		lane := timelineLane{Job: job}
		if job.Schedule != nil {
			if sched, err := schedule.Parse(*job.Schedule); err == nil {
				var expected []string
				for t := sched.Next(from.In(time.Local)); !t.IsZero() && t.Before(to); t = sched.Next(t) {
					if len(expected) == maxExpectedTicks {
						expected = nil
						break
					}
					expected = append(expected, format(pct(t)))
				}
				lane.Expected = expected
			}
		}
		for _, c := range crontabChanges[job.Slug] {
			title := "Crontab changed · " + c.TakenAt.Local().Format("Jan 2 15:04")
			if c.Kind == "schedule" {
				title += " · schedule " + c.Before + " → " + c.After
			} else {
				title += " · line " + c.Kind
			}
			lane.Changes = append(lane.Changes, timelineChange{X: format(pct(c.TakenAt)), Title: title})
		}
		counts := map[string]int{}
		for _, run := range byJob[job.ID] {
			end := to
			if run.EndedAt != nil {
				end = *run.EndedAt
			}
			x, w := span(run.StartedAt, end, minMarkPct)
			title := fmt.Sprintf("%s · %s", statusLabel(run.Status), run.StartedAt.Local().Format("Jan 2 15:04:05"))
			if run.DurationMS != nil {
				title += " · " + humanDuration(time.Duration(*run.DurationMS)*time.Millisecond)
			}
			if run.ExitCode != nil && *run.ExitCode != 0 {
				title += fmt.Sprintf(" · exit %d", *run.ExitCode)
			}
			if run.Reason != "" {
				title += " · " + run.Reason
			}
			lane.Marks = append(lane.Marks, timelineMark{Kind: markKind(run.Status), X: x, W: w, Title: title,
				Href: "/runs/" + run.ID, Overlaps: run.OverlappedRunID != ""})
			counts[statusLabel(run.Status)]++
		}
		for _, at := range missed[job.ID] {
			x, w := span(at, at, minMissedPct)
			lane.Marks = append(lane.Marks, timelineMark{Kind: "missed", X: x, W: w,
				Title: "Missed · expected " + at.Local().Format("Jan 2 15:04"), Href: "/jobs/" + job.ID})
			counts["Missed"]++
		}
		lane.Summary = laneSummary(job.Name, counts)
		tl.Lanes = append(tl.Lanes, lane)
	}
	s.render(w, "timeline.html", page{Title: "Timeline", Tab: "timeline", Data: tl})
}

// timelineTicks returns axis labels: every 3 hours for a day, every midnight
// for a week, in local time.
func timelineTicks(from, to time.Time, window time.Duration) []timelineTick {
	step, layout := 3*time.Hour, "15:04"
	if window > 24*time.Hour {
		step, layout = 24*time.Hour, "Mon 2"
	}
	local := from.Local()
	t := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local)
	var ticks []timelineTick
	for ; t.Before(to); t = nextTick(t, step) {
		if p := float64(t.Sub(from)) / float64(window) * 100; t.After(from) && p < nowGapPct {
			ticks = append(ticks, timelineTick{Pct: fmt.Sprintf("%.3f%%", p), Label: t.Format(layout)})
		}
	}
	return ticks
}

// nextTick adds step in calendar terms, so days stay aligned to midnight
// across daylight-saving changes.
func nextTick(t time.Time, step time.Duration) time.Time {
	if step == 24*time.Hour {
		return t.AddDate(0, 0, 1)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+int(step/time.Hour), 0, 0, 0, time.Local)
}

func laneSummary(name string, counts map[string]int) string {
	if len(counts) == 0 {
		return name + ": no runs in this period"
	}
	var parts []string
	for _, label := range []string{"Success", "Failed", "Timed out", "Missed", "Running", "Skipped", "Cancelled"} {
		if n := counts[label]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ToLower(label)))
		}
	}
	return name + ": " + strings.Join(parts, ", ")
}
