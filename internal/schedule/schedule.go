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

// Validate rejects expressions that parse but never fire, such as "0 0 30 2 *".
func Validate(expr string, now time.Time) error {
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
