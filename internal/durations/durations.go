// Package durations judges whether a run was unusually slow and whether a job
// is getting slower, from the durations of its successful runs. Only
// successful runs count: a failure that exits early would skew the baseline.
package durations

import (
	"slices"
	"time"
)

const (
	// Baseline is how many earlier successful runs set a run's usual duration.
	Baseline = 20
	// MinSamples is the fewest runs a comparison needs.
	MinSamples = 5
	// SlowFactor is how many times the usual duration counts as slow.
	SlowFactor = 3
	// DriftRatio is how much slower the last week must be than the month
	// before it to count as getting slower.
	DriftRatio = 1.4
	// MinExtra keeps short jobs from being flagged for a few seconds' jitter.
	MinExtra = 10 * time.Second
	// RecentWindow and PriorWindow are the periods drift compares.
	RecentWindow = 7 * 24 * time.Hour
	PriorWindow  = 30 * 24 * time.Hour
)

// Median returns the middle duration, or 0 for none.
func Median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Slowness describes a run that took much longer than usual.
type Slowness struct {
	Usual  time.Duration // median of the earlier successful runs
	Factor float64       // how many times the usual it took
}

// Slow reports whether d is unusually slow compared with earlier, the
// durations of the job's previous successful runs.
func Slow(d time.Duration, earlier []time.Duration) (Slowness, bool) {
	if len(earlier) < MinSamples {
		return Slowness{}, false
	}
	usual := Median(earlier)
	if usual <= 0 || d < SlowFactor*usual || d-usual < MinExtra {
		return Slowness{}, false
	}
	return Slowness{Usual: usual, Factor: float64(d) / float64(usual)}, true
}

// Point is one successful run's start and duration.
type Point struct {
	Start    time.Time
	Duration time.Duration
}

// Drift describes a job whose runs have become slower.
type Drift struct {
	Recent, Prior time.Duration // medians of the last week and the month before
	Percent       int           // how much slower, e.g. 52 for +52%
}

// Drifting reports whether the median of the runs in the last RecentWindow
// before now is at least DriftRatio times the median of the PriorWindow
// before that.
func Drifting(points []Point, now time.Time) (Drift, bool) {
	recentFrom := now.Add(-RecentWindow)
	priorFrom := recentFrom.Add(-PriorWindow)
	var recent, prior []time.Duration
	for _, p := range points {
		switch {
		case !p.Start.Before(recentFrom) && !p.Start.After(now):
			recent = append(recent, p.Duration)
		case !p.Start.Before(priorFrom) && p.Start.Before(recentFrom):
			prior = append(prior, p.Duration)
		}
	}
	if len(recent) < MinSamples || len(prior) < MinSamples {
		return Drift{}, false
	}
	d := Drift{Recent: Median(recent), Prior: Median(prior)}
	if d.Prior <= 0 || float64(d.Recent) < DriftRatio*float64(d.Prior) || d.Recent-d.Prior < MinExtra {
		return Drift{}, false
	}
	d.Percent = int((float64(d.Recent)/float64(d.Prior) - 1) * 100)
	return d, true
}
