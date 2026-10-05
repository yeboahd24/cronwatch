package systemd

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/schedule"
)

// shortcuts are systemd's named calendar events as cron expressions.
var shortcuts = map[string]string{
	"minutely": "* * * * *", "hourly": "0 * * * *", "daily": "0 0 * * *",
	"weekly": "0 0 * * 1", "monthly": "0 0 1 * *", "yearly": "0 0 1 1 *", "annually": "0 0 1 1 *",
}

var weekdays = map[string]string{"mon": "1", "tue": "2", "wed": "3", "thu": "4", "fri": "5", "sat": "6", "sun": "0"}

var cronField = regexp.MustCompile(`^(\*|[0-9]+)(/[0-9]+)?$`)

// CalendarToCron converts a systemd OnCalendar expression to a five-field
// cron expression, when cron can express it: whole minutes (seconds 0), no
// years, local time. ok is false otherwise.
func CalendarToCron(calendar string) (expr string, ok bool) {
	calendar = strings.TrimSpace(calendar)
	if c, found := shortcuts[strings.ToLower(calendar)]; found {
		return c, true
	}
	parts := strings.Fields(calendar)
	var dow, date, clock string
	switch len(parts) {
	case 1: // "*-*-* 06:00" without a date is "06:00"
		clock = parts[0]
	case 2:
		if strings.Contains(parts[0], "-") {
			date, clock = parts[0], parts[1]
		} else {
			dow, clock = parts[0], parts[1]
		}
	case 3:
		dow, date, clock = parts[0], parts[1], parts[2]
	default:
		return "", false // includes time zones such as "... UTC"
	}
	month, day := "*", "*"
	if date != "" {
		d := strings.Split(date, "-")
		if len(d) != 3 || d[0] != "*" {
			return "", false // a specific year cannot be expressed
		}
		var ok1, ok2 bool
		if month, ok1 = cronList(d[1], false); !ok1 {
			return "", false
		}
		if day, ok2 = cronList(d[2], false); !ok2 {
			return "", false
		}
	}
	t := strings.Split(clock, ":")
	if len(t) < 2 || len(t) > 3 {
		return "", false
	}
	if len(t) == 3 && strings.TrimLeft(t[2], "0") != "" {
		return "", false // cron has no seconds
	}
	hour, ok1 := cronList(t[0], false)
	minute, ok2 := cronList(t[1], false)
	if !ok1 || !ok2 {
		return "", false
	}
	weekday := "*"
	if dow != "" {
		var ok bool
		if weekday, ok = cronList(dow, true); !ok {
			return "", false
		}
	}
	expr = strings.Join([]string{minute, hour, day, month, weekday}, " ")
	if schedule.Validate(expr, time.Now()) != nil {
		return "", false
	}
	return expr, true
}

// cronList converts a systemd list such as "06,18", "Mon..Fri" or "0/15" to
// cron syntax.
func cronList(field string, names bool) (string, bool) {
	var out []string
	for item := range strings.SplitSeq(field, ",") {
		if lo, hi, isRange := strings.Cut(item, ".."); isRange {
			// A stepped range is rare and its meaning is easy to get wrong;
			// no conversion is better than a wrong one.
			if strings.Contains(item, "/") {
				return "", false
			}
			a, ok1 := cronValue(lo, names)
			b, ok2 := cronValue(hi, names)
			if !ok1 || !ok2 {
				return "", false
			}
			out = append(out, a+"-"+b)
			continue
		}
		v, ok := cronValue(item, names)
		if !ok {
			return "", false
		}
		out = append(out, v)
	}
	return strings.Join(out, ","), true
}

func cronValue(v string, names bool) (string, bool) {
	if names {
		if n, ok := weekdays[strings.ToLower(v)]; ok {
			return n, true
		}
		if len(v) >= 3 {
			if n, ok := weekdays[strings.ToLower(v[:3])]; ok {
				return n, true
			}
		}
		return "", false
	}
	if !cronField.MatchString(v) {
		return "", false
	}
	// Drop leading zeros: "06" is 6.
	base, step, _ := strings.Cut(v, "/")
	if base != "*" {
		n, _ := strconv.Atoi(base)
		base = strconv.Itoa(n)
	}
	if step != "" {
		return base + "/" + step, true
	}
	return base, true
}
