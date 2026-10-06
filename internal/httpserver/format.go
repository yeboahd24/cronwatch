package httpserver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var statusLabels = map[string]string{
	"success":          "Success",
	"failed":           "Failed",
	"missed":           "Missed",
	"running":          "Running",
	"cancelled":        "Cancelled",
	"timeout":          "Timed out",
	"skipped":          "Skipped",
	"never_run":        "Never run",
	"invalid_schedule": "Invalid schedule",
	"recovered":        "Recovered",
	"paused":           "Paused",
	"archived":         "Archived",
}

func statusLabel(status string) string {
	if label, ok := statusLabels[status]; ok {
		return label
	}
	return status
}

// shortTime formats t relative to now's calendar day in local time:
// "14:05", "Yesterday 14:05", "Tomorrow 02:00", "Oct 2 14:05", or
// "2025-12-31" for other years.
func shortTime(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.Local) }
	switch days := int(day(t).Sub(day(now)).Round(24*time.Hour) / (24 * time.Hour)); {
	case days == 0:
		return t.Format("15:04")
	case days == -1:
		return "Yesterday " + t.Format("15:04")
	case days == 1:
		return "Tomorrow " + t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("Jan 2 15:04")
	default:
		return t.Format("2006-01-02")
	}
}

// fullTime is the exact timestamp shown on hover and in details.
func fullTime(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05 MST") }

// humanDuration formats d compactly: "<1ms", "8ms", "0.8s", "42s", "3m 12s", "1h 4m".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < 100*time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + "s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		m, s := int(d.Minutes()), int(d.Seconds())%60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

// humanKB formats a size in kilobytes: "512 KB", "42 MB", "1.5 GB".
func humanKB(kb int64) string {
	switch {
	case kb < 1024:
		return fmt.Sprintf("%d KB", kb)
	case kb < 10*1024:
		return strconv.FormatFloat(float64(kb)/1024, 'f', 1, 64) + " MB"
	case kb < 1024*1024:
		return fmt.Sprintf("%d MB", kb/1024)
	default:
		return strconv.FormatFloat(float64(kb)/(1024*1024), 'f', 1, 64) + " GB"
	}
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellCommand renders a stored command (Go-quoted arguments joined by
// spaces) the way it would be typed in a POSIX shell. Unparseable values are
// returned unchanged.
func shellCommand(stored string) string {
	var parts []string
	rest := strings.TrimSpace(stored)
	for rest != "" {
		quoted, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return stored
		}
		arg, err := strconv.Unquote(quoted)
		if err != nil {
			return stored
		}
		if shellSafe.MatchString(arg) {
			parts = append(parts, arg)
		} else {
			parts = append(parts, "'"+strings.ReplaceAll(arg, "'", `'\''`)+"'")
		}
		rest = strings.TrimSpace(rest[len(quoted):])
	}
	return strings.Join(parts, " ")
}
