package schedule

import (
	"testing"
	"time"
)

func TestDescriptorsMatchTheirExpressions(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 30, 0, 0, time.Local)
	for desc, expr := range map[string]string{
		"@yearly":   "0 0 1 1 *",
		"@annually": "0 0 1 1 *",
		"@monthly":  "0 0 1 * *",
		"@weekly":   "0 0 * * 0",
		"@daily":    "0 0 * * *",
		"@midnight": "0 0 * * *",
		"@hourly":   "0 * * * *",
	} {
		if err := Validate(desc, now); err != nil {
			t.Errorf("Validate(%q) = %v", desc, err)
			continue
		}
		got, _ := Next(desc, now)
		want, _ := Next(expr, now)
		if !got.Equal(want) {
			t.Errorf("Next(%q) = %v, want %v as for %q", desc, got, want, expr)
		}
	}
}

func TestRejectsSchedulesWithoutFixedTimes(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 30, 0, 0, time.Local)
	for _, expr := range []string{"@every 1h", "@reboot", "@bogus", "0 0 30 2 *"} {
		if err := Validate(expr, now); err == nil {
			t.Errorf("Validate(%q) = nil, want an error", expr)
		}
	}
}

func TestTimeZonePrefix(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	got, err := Next("CRON_TZ=Asia/Tokyo 0 2 * * *", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 8, 17, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}
