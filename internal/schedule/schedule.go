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
		cron.Dow |
		cron.Descriptor,
)

func Parse(expr string) (cron.Schedule, error) {
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	// "@every" counts from whenever it is first asked, so it has no expected
	// times to miss; cron does not accept it either.
	if _, ok := s.(cron.ConstantDelaySchedule); ok {
		return nil, fmt.Errorf("invalid cron expression %q: @every is not a cron schedule", expr)
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
