package durations

import (
	"testing"
	"time"
)

func secs(ns ...int) []time.Duration {
	out := make([]time.Duration, len(ns))
	for i, n := range ns {
		out[i] = time.Duration(n) * time.Second
	}
	return out
}

func TestMedian(t *testing.T) {
	if got := Median(secs(5, 1, 3)); got != 3*time.Second {
		t.Errorf("odd median = %s", got)
	}
	if got := Median(secs(4, 1, 3, 2)); got != 2500*time.Millisecond {
		t.Errorf("even median = %s", got)
	}
	if Median(nil) != 0 {
		t.Error("median of nothing")
	}
}

func TestSlow(t *testing.T) {
	usual := secs(40, 42, 41, 45, 39, 300) // one earlier outlier does not move the median
	if s, ok := Slow(130*time.Second, usual); !ok || s.Usual != 41500*time.Millisecond || s.Factor < 3.1 || s.Factor > 3.2 {
		t.Fatalf("Slow(130s) = %+v, %v", s, ok)
	}
	if _, ok := Slow(100*time.Second, usual); ok {
		t.Error("2.4x the usual counted as slow")
	}
	if _, ok := Slow(time.Second, []time.Duration{200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond, 200 * time.Millisecond}); ok {
		t.Error("a 1s run of a 0.2s job counted as slow")
	}
	if _, ok := Slow(time.Hour, secs(1, 1, 1, 1)); ok {
		t.Error("judged with fewer than MinSamples earlier runs")
	}
}

func TestDrifting(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	var points []Point
	add := func(daysAgo int, d time.Duration) {
		points = append(points, Point{now.Add(-time.Duration(daysAgo) * day), d})
	}
	for i := 8; i <= 37; i++ {
		add(i, 120*time.Second) // a month at 2m
	}
	for i := range 7 {
		add(i, 180*time.Second) // a week at 3m: +50%
	}
	d, ok := Drifting(points, now)
	if !ok || d.Recent != 180*time.Second || d.Prior != 120*time.Second || d.Percent != 50 {
		t.Fatalf("Drifting = %+v, %v", d, ok)
	}
	if _, ok := Drifting(points[:30], now); ok {
		t.Error("drift reported without recent runs")
	}
	steady := append([]Point(nil), points[:30]...)
	for i := range 7 {
		steady = append(steady, Point{now.Add(-time.Duration(i) * day), 130 * time.Second})
	}
	if _, ok := Drifting(steady, now); ok {
		t.Error("+8% reported as drift")
	}
	var short []Point
	for i := range 37 {
		d := time.Second
		if i < 7 {
			d = 3 * time.Second
		}
		short = append(short, Point{now.Add(-time.Duration(i) * day), d})
	}
	if _, ok := Drifting(short, now); ok {
		t.Error("1s to 3s reported as drift despite MinExtra")
	}
}
