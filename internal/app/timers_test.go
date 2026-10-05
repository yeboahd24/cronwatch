package app

import (
	"testing"

	"github.com/yeboahd24/cronwatch/internal/systemd"
)

func TestTimerFormatting(t *testing.T) {
	two := 2
	for _, tc := range []struct {
		timer          systemd.Timer
		schedule, cron string
		result         string
	}{
		{systemd.Timer{Calendar: []string{"*-*-* 06,18:00:00"}, Result: "success"}, "*-*-* 06,18:00:00", "0 6,18 * * *", "success"},
		{systemd.Timer{Calendar: []string{"daily"}, Monotonic: []string{"OnBootUSec=5min"}}, "daily; OnBootUSec=5min", "", "never run"},
		{systemd.Timer{Calendar: []string{"*-*-* 06:00:30"}, Result: "exit-code", ExitStatus: &two}, "*-*-* 06:00:30", "", "exit-code (exit 2)"},
		{systemd.Timer{Result: "timeout"}, "—", "", "timeout"},
	} {
		cron, _ := timerCron(tc.timer)
		if got := timerSchedule(tc.timer); got != tc.schedule || cron != tc.cron || timerResult(tc.timer) != tc.result {
			t.Errorf("%+v: schedule %q cron %q result %q", tc.timer, got, cron, timerResult(tc.timer))
		}
	}
}
