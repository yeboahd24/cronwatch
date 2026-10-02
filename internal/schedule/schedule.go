package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

var parser = cron.NewParser(
	cron.Minute |
		cron.Hour |
		cron.Dom |
		cron.Month |
		cron.Dow,
)

func Parse(expr string) (cron.Schedule, error) {
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return s, nil
}

func Next(expr string, after time.Time) (time.Time, error) {
	s, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(after), nil
}

// Previous returns the last occurrence strictly before before. It expands the
// search window for infrequent expressions without scanning every minute.
func Previous(expr string, before time.Time) (time.Time, error) {
	s, err := Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	window := 24 * time.Hour
	for window <= 8*366*24*time.Hour {
		cursor := before.Add(-window)
		var previous time.Time
		for {
			next := s.Next(cursor)
			if next.IsZero() || !next.Before(before) {
				break
			}
			previous = next
			cursor = next
		}
		if !previous.IsZero() {
			return previous, nil
		}
		window *= 2
	}
	return time.Time{}, fmt.Errorf("no occurrence of %q found in five years", expr)
}
