// Package logs defines the stored combined-log format. Each line written by
// the child's stderr starts with StderrMark; stdout lines are stored as-is,
// so logs recorded before the mark existed read as plain stdout.
package logs

import (
	"regexp"
	"strings"
)

// StderrMark prefixes stderr lines in a stored combined log.
const StderrMark = '\x02'

// Stream identifies where a log line came from.
type Stream int

const (
	Stdout Stream = iota
	Stderr
	// Truncated marks the placeholder for output dropped by the capture limit.
	Truncated
)

// Line is one displayable line of a combined log.
type Line struct {
	Number int // 1-based position in the stored log; 0 for the truncation marker
	Stream Stream
	Text   string
}

func (l Line) IsStderr() bool    { return l.Stream == Stderr }
func (l Line) IsTruncated() bool { return l.Stream == Truncated }

var truncationMarker = regexp.MustCompile(`^\.\.\. \[cronwatch: \d+ bytes truncated\] \.\.\.$`)

// Parse splits a stored combined log into lines.
func Parse(combined string) []Line {
	combined = strings.TrimSuffix(combined, "\n")
	if combined == "" {
		return nil
	}
	raw := strings.Split(combined, "\n")
	lines := make([]Line, 0, len(raw))
	number := 0
	for _, text := range raw {
		line := Line{Stream: Stdout, Text: text}
		switch {
		case truncationMarker.MatchString(text):
			line.Stream = Truncated
		case strings.HasPrefix(text, string(StderrMark)):
			line.Stream = Stderr
			line.Text = text[1:]
		}
		if !line.IsTruncated() {
			number++
			line.Number = number
		}
		lines = append(lines, line)
	}
	return lines
}

// ParseAs splits a single-stream log (stored stdout or stderr) into lines
// attributed to stream.
func ParseAs(text string, stream Stream) []Line {
	lines := Parse(text)
	for i := range lines {
		if !lines[i].IsTruncated() {
			lines[i].Stream = stream
		}
	}
	return lines
}

// Plain returns a stored combined log without stream marks.
func Plain(combined string) string {
	return strings.ReplaceAll(strings.TrimPrefix(combined, string(StderrMark)), "\n"+string(StderrMark), "\n")
}

// Tail returns the last n lines.
func Tail(lines []Line, n int) []Line {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// LastError returns the last non-blank stderr line, if any.
func LastError(lines []Line) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].IsStderr() && strings.TrimSpace(lines[i].Text) != "" {
			return lines[i].Text
		}
	}
	return ""
}

// Filter keeps lines from the given stream (stdout or stderr) and lines that
// contain query, ignoring case. An empty query matches everything.
func Filter(lines []Line, onlyStderr bool, query string) []Line {
	query = strings.ToLower(query)
	var out []Line
	for _, l := range lines {
		if l.IsTruncated() {
			if !onlyStderr && query == "" {
				out = append(out, l)
			}
			continue
		}
		if onlyStderr && !l.IsStderr() {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(l.Text), query) {
			continue
		}
		out = append(out, l)
	}
	return out
}
