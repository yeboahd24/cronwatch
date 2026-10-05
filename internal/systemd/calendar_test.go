package systemd

import "testing"

func TestCalendarToCron(t *testing.T) {
	for in, want := range map[string]string{
		"daily":                 "0 0 * * *",
		"weekly":                "0 0 * * 1",
		"*-*-* 06,18:00:00":     "0 6,18 * * *",
		"*-*-* 07..23:30:00":    "30 7-23 * * *",
		"Mon..Fri *-*-* 09:15":  "15 9 * * 1-5",
		"Sat,Sun 03:00":         "0 3 * * 6,0",
		"*-*-01 04:00:00":       "0 4 1 * *",
		"*-01,07-01 00:00:00":   "0 0 1 1,7 *",
		"*-*-* *:0/15:00":       "0/15 * * * *",
		"Monday *-*-* 02:00:00": "0 2 * * 1",
	} {
		if got, ok := CalendarToCron(in); !ok || got != want {
			t.Errorf("CalendarToCron(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{
		"*-*-* 06:00:30",      // seconds
		"2026-*-* 06:00:00",   // a year
		"*-*-* 06:00:00 UTC",  // a time zone
		"*-*-* 6..18/2:00:00", // a stepped range
		"*-*~03 00:00:00",     // last days of the month
		"Mon..Fri 9",          // not a time
	} {
		if got, ok := CalendarToCron(in); ok {
			t.Errorf("CalendarToCron(%q) = %q; want no equivalent", in, got)
		}
	}
}
