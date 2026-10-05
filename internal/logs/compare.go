package logs

import (
	"regexp"
	"strings"
)

// Patterns for the parts of a log line that change from run to run without
// meaning anything changed: IDs, durations, timestamps and counters, day and
// month names, and temporary paths. Earlier patterns win: a UUID is masked
// whole before its parts could match anything else.
var (
	uuid      = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	hexID     = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	duration  = regexp.MustCompile(`\b(?:[0-9]+(?:\.[0-9]+)?(?:ns|us|µs|ms|s|m|h|d))+\b`)
	digits    = regexp.MustCompile(`[0-9]+`)
	dayMonth  = regexp.MustCompile(`\b(Mon|Tue|Wed|Thu|Fri|Sat|Sun|Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)[a-z]*\b`)
	tempPath  = regexp.MustCompile(`/tmp/[^\s'"]*`)
	spaceRuns = regexp.MustCompile(`\s+`)
)

// Normalize returns the comparable form of a log line: IDs, durations,
// numbers, day and month names and /tmp paths are masked and whitespace
// collapsed, so "backup 2026-10-05 took 42s" and "backup 2026-10-06 took 1m3s"
// compare equal.
func Normalize(text string) string {
	text = tempPath.ReplaceAllString(text, "/tmp/…")
	text = uuid.ReplaceAllString(text, "‹id›")
	text = hexID.ReplaceAllString(text, "‹id›")
	text = duration.ReplaceAllString(text, "‹duration›")
	text = digits.ReplaceAllString(text, "#")
	text = dayMonth.ReplaceAllString(text, "‹date›")
	return strings.TrimSpace(spaceRuns.ReplaceAllString(text, " "))
}

// Compare returns the lines of after that before does not have (added) and
// the lines of before that after does not have (removed), comparing
// normalized text and stream and counting repeats, in their original order.
// Blank lines and truncation markers are ignored.
func Compare(before, after []Line) (added, removed []Line) {
	key := func(l Line) string { return string(rune('0'+l.Stream)) + Normalize(l.Text) }
	counts := func(lines []Line) map[string]int {
		m := map[string]int{}
		for _, l := range lines {
			if !l.IsTruncated() && strings.TrimSpace(l.Text) != "" {
				m[key(l)]++
			}
		}
		return m
	}
	unmatched := func(lines []Line, other map[string]int) []Line {
		var out []Line
		for _, l := range lines {
			if l.IsTruncated() || strings.TrimSpace(l.Text) == "" {
				continue
			}
			if k := key(l); other[k] > 0 {
				other[k]--
			} else {
				out = append(out, l)
			}
		}
		return out
	}
	return unmatched(after, counts(before)), unmatched(before, counts(after))
}
