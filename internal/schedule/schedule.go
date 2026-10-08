package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(
	cron.Minute |
		cron.Hour |
		cron.Dom |
		cron.Month |
		cron.Dow |
		cron.Descriptor,
)

func Parse(expr string) (cron.Schedule, error) {
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	// robfig's "@every" counts from whenever it is asked, so it has no
	// expected times to miss; ForJob anchors it to the job's runs.
	if _, ok := s.(cron.ConstantDelaySchedule); ok {
		return nil, fmt.Errorf("invalid cron expression %q: @every needs the job's last run; use ForJob", expr)
	}
	return s, nil
}

// MinEvery is the shortest period an "@every" schedule may have: missed
// runs are checked once a minute.
const MinEvery = time.Minute

// Every returns the schedule expression for a job expected at least once
// every d, such as "@every 1h".
func Every(d time.Duration) string {
	text := d.String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return "@every " + text
}

// ParseEvery reads an "@every DURATION" expression. ok is false for any
// other expression.
func ParseEvery(expr string) (d time.Duration, ok bool, err error) {
	rest, ok := strings.CutPrefix(expr, "@every ")
	if !ok {
		return 0, false, nil
	}
	d, err = time.ParseDuration(strings.TrimSpace(rest))
	switch {
	case err != nil:
		return 0, true, fmt.Errorf("invalid schedule %q: %q is not a duration such as 1h", expr, rest)
	case d < MinEvery:
		return 0, true, fmt.Errorf("invalid schedule %q: the period must be at least %s", expr, MinEvery)
	case d%time.Second != 0:
		return 0, true, fmt.Errorf("invalid schedule %q: the period must be whole seconds", expr)
	}
	return d, true, nil
}

// ForJob returns the schedule a job with expression expr is expected on.
// An "@every" job is expected every period after anchor, the start of its
// last run or when it was last checked, whichever is later; any other
// expression is a cron schedule, and anchor is not used.
func ForJob(expr string, anchor time.Time) (cron.Schedule, error) {
	d, ok, err := ParseEvery(expr)
	if err != nil {
		return nil, err
	}
	if ok {
		return periodic{every: d, anchor: anchor}, nil
	}
	return Parse(expr)
}

// periodic is due every period after anchor.
type periodic struct {
	every  time.Duration
	anchor time.Time
}

// Next returns the first due time after t.
func (p periodic) Next(t time.Time) time.Time {
	if t.Before(p.anchor) {
		return p.anchor.Add(p.every)
	}
	n := t.Sub(p.anchor)/p.every + 1
	return p.anchor.Add(n * p.every)
}

// Validate rejects expressions that parse but never fire, such as "0 0 30 2 *".
// It accepts "@every DURATION" for a period of at least MinEvery.
func Validate(expr string, now time.Time) error {
	if _, ok, err := ParseEvery(expr); ok {
		return err
	}
	s, err := Parse(expr)
	if err != nil {
		return err
	}
	if s.Next(now).IsZero() {
		return fmt.Errorf("invalid cron expression %q: never occurs", expr)
	}
	return nil
}

func Next(expr string, after time.Time) (time.Time, error) {
	s, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}
