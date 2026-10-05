package httpserver

import (
	"testing"
	"time"
)

func TestShortTime(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-2 * time.Hour), "10:00"},
		{time.Date(2026, 10, 1, 23, 30, 0, 0, time.Local), "Yesterday 23:30"},
		{time.Date(2026, 10, 3, 2, 0, 0, 0, time.Local), "Tomorrow 02:00"},
		{time.Date(2026, 9, 14, 8, 5, 0, 0, time.Local), "Sep 14 08:05"},
		{time.Date(2025, 12, 31, 8, 5, 0, 0, time.Local), "2025-12-31"},
	} {
		if got := shortTime(tc.t, now); got != tc.want {
			t.Errorf("shortTime(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		300 * time.Microsecond:  "<1ms",
		8 * time.Millisecond:    "8ms",
		800 * time.Millisecond:  "0.8s",
		2100 * time.Millisecond: "2.1s",
		42 * time.Second:        "42s",
		5 * time.Minute:         "5m",
		192 * time.Second:       "3m 12s",
		64 * time.Minute:        "1h 4m",
		2 * time.Hour:           "2h",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	for stored, want := range map[string]string{
		`"/opt/scripts/backup.sh" "--full"`: `/opt/scripts/backup.sh --full`,
		`"sh" "-c" "echo \"hi\"; exit 2"`:   `sh -c 'echo "hi"; exit 2'`,
		`"echo" "it's"`:                     `echo 'it'\''s'`,
		`"printf" ""`:                       `printf ''`,
		`not quoted`:                        `not quoted`,
	} {
		if got := shellCommand(stored); got != want {
			t.Errorf("shellCommand(%s) = %s, want %s", stored, got, want)
		}
	}
}

func TestStatusLabel(t *testing.T) {
	if statusLabel("never_run") != "Never run" || statusLabel("failed") != "Failed" || statusLabel("weird") != "weird" {
		t.Fatal("unexpected labels")
	}
}

func TestHumanKB(t *testing.T) {
	for kb, want := range map[int64]string{0: "0 KB", 512: "512 KB", 1536: "1.5 MB", 43008: "42 MB", 1572864: "1.5 GB"} {
		if got := humanKB(kb); got != want {
			t.Errorf("humanKB(%d) = %q, want %q", kb, got, want)
		}
	}
}
