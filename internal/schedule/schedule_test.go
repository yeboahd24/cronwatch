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
	for _, expr := range []string{"@reboot", "@bogus", "0 0 30 2 *", "@every 30s", "@every 1.5s", "@every soon"} {
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

func TestEvery(t *testing.T) {
	if got := Every(90 * time.Minute); got != "@every 1h30m" {
		t.Errorf("Every(90m) = %q", got)
	}
	if got := Every(2 * time.Hour); got != "@every 2h" {
		t.Errorf("Every(2h) = %q", got)
	}
	if err := Validate("@every 1h", time.Now()); err != nil {
		t.Errorf("Validate(@every 1h) = %v", err)
	}
	// Without an anchor, @every has nothing to count from.
	if _, err := Parse("@every 1h"); err == nil {
		t.Error("Parse accepted @every")
	}
}

func TestForJobAnchorsEvery(t *testing.T) {
	anchor := time.Date(2026, 10, 8, 12, 10, 0, 0, time.UTC)
	s, err := ForJob("@every 1h", anchor)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ after, want time.Time }{
		{anchor.Add(-time.Hour), anchor.Add(time.Hour)},
		{anchor, anchor.Add(time.Hour)},
		{anchor.Add(59 * time.Minute), anchor.Add(time.Hour)},
		{anchor.Add(time.Hour), anchor.Add(2 * time.Hour)},
		{anchor.Add(150 * time.Minute), anchor.Add(3 * time.Hour)},
	} {
		if got := s.Next(c.after); !got.Equal(c.want) {
			t.Errorf("Next(%v) = %v, want %v", c.after, got, c.want)
		}
	}
	cron, err := ForJob("0 2 * * *", anchor)
	if err != nil {
		t.Fatal(err)
	}
	if got := cron.Next(anchor); got.Hour() != 2 {
		t.Errorf("a cron job's Next = %v", got)
	}
}
