package httpserver

import (
	"fmt"
	"time"

	"github.com/yeboahd24/cronwatch/internal/durations"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

const (
	listSparkRuns = 30 // runs in a jobs-list sparkline
	jobSparkRuns  = 60 // runs in a job page sparkline
)

// sparkBar is one run in a duration sparkline, positioned in SVG percentages.
type sparkBar struct {
	X, W, Y, H string
	Kind       string // ok, slow or fail
	Title      string
	Href       string
}

// jobTrend is a job's durations prepared for display.
type jobTrend struct {
	storage.Trend
	Bars    []sparkBar
	Summary string // the sparkline in words, for screen readers
}

// SlowRun returns how slow run id was, if it was unusually slow.
func (t *jobTrend) SlowRun(id string) *durations.Slowness {
	if t == nil {
		return nil
	}
	if sl, ok := t.Slow[id]; ok {
		return &sl
	}
	return nil
}

func newJobTrend(t storage.Trend, name string) *jobTrend {
	jt := &jobTrend{Trend: t}
	// A handful of runs says nothing about a trend.
	if len(t.Runs) < durations.MinSamples {
		return jt
	}
	var longest time.Duration
	for _, r := range t.Runs {
		longest = max(longest, r.Duration)
	}
	// Leave headroom above the usual duration, so a steady job draws at half
	// height and slow runs stand out above it.
	scale := max(longest, 2*t.Usual)
	slots := max(len(t.Runs), 12) // a few runs still draw as narrow bars
	slot := 100.0 / float64(slots)
	slow, failed := 0, 0
	for i, r := range t.Runs {
		h := 100.0
		if scale > 0 {
			h = max(float64(r.Duration)/float64(scale)*100, 6) // keep tiny runs visible
		}
		b := sparkBar{
			X: fmt.Sprintf("%.2f%%", float64(i)*slot+slot*0.15), W: fmt.Sprintf("%.2f%%", slot*0.7),
			Y: fmt.Sprintf("%.2f%%", 100-h), H: fmt.Sprintf("%.2f%%", h),
			Kind: "ok", Href: "/runs/" + r.ID,
			Title: fmt.Sprintf("%s · %s · %s", r.StartedAt.Local().Format("Jan 2 15:04"), humanDuration(r.Duration), statusLabel(r.Status)),
		}
		if sl, ok := t.Slow[r.ID]; ok {
			b.Kind = "slow"
			b.Title += fmt.Sprintf(" · %.1f× the usual %s", sl.Factor, humanDuration(sl.Usual))
			slow++
		} else if model.Failing(r.Status) {
			b.Kind = "fail"
			failed++
		}
		jt.Bars = append(jt.Bars, b)
	}
	jt.Summary = fmt.Sprintf("%s: durations of the last %d runs, longest %s", name, len(t.Runs), humanDuration(longest))
	if t.Usual > 0 {
		jt.Summary += ", usually " + humanDuration(t.Usual)
	}
	if slow > 0 {
		jt.Summary += fmt.Sprintf(", %d unusually slow", slow)
	}
	if failed > 0 {
		jt.Summary += fmt.Sprintf(", %d failed", failed)
	}
	return jt
}
