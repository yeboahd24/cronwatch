package systemd

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Output captured from systemd 257.
const (
	fakeUnits = `anacron.timer                loaded active   waiting Trigger anacron every hour
apt-daily.timer              masked inactive dead    apt-daily.timer
fstrim.timer                 loaded active   waiting Discard unused filesystem blocks once a week
`
	fakeTimers = `Unit=anacron.service
TimersCalendar={ OnCalendar=*-*-* 07..23:30:00 ; next_elapse=@1791203400 }
TimersMonotonic=
NextElapseUSecRealtime=Mon 2026-10-05 12:30:03 UTC
LastTriggerUSec=Mon 2026-10-05 11:32:14 UTC
Id=anacron.timer
Description=Trigger anacron every hour
LoadState=loaded
ActiveState=active

Unit=apt-daily.service
TimersCalendar=
TimersMonotonic=
NextElapseUSecRealtime=
LastTriggerUSec=n/a
Id=apt-daily.timer
Description=apt-daily.timer
LoadState=masked
ActiveState=inactive

Unit=fstrim.service
TimersCalendar={ OnCalendar=Mon *-*-* 00:00:00 ; next_elapse=@1791763200 }
TimersMonotonic={ OnUnitActiveSec=1w ; next_elapse=@1791763200 }
NextElapseUSecRealtime=Mon 2026-10-12 01:06:07 UTC
LastTriggerUSec=Mon 2026-10-05 06:51:14 UTC
Id=fstrim.timer
Description=Discard unused filesystem blocks once a week
LoadState=loaded
ActiveState=active
`
	fakeServices = `Result=success
ExecMainStartTimestamp=@1791199934
ExecMainStatus=0
ExecStart={ path=/usr/sbin/anacron ; argv[]=/usr/sbin/anacron -d -q $ANACRON_ARGS ; ignore_errors=no ; start_time=[@1791199934] ; stop_time=[@1791199934] ; pid=157919 ; code=exited ; status=0 }
Id=anacron.service

Result=success
ExecMainStartTimestamp=
ExecMainStatus=0
ExecStart=
Id=apt-daily.service

Result=exit-code
ExecMainStartTimestamp=@1791183074
ExecMainStatus=32
ExecStart={ path=/sbin/fstrim ; argv[]=/sbin/fstrim --listed-in /etc/fstab --verbose ; ignore_errors=no ; start_time=[@1791183074] ; stop_time=[@1791183080] ; pid=812 ; code=exited ; status=32 }
Id=fstrim.service
`
)

func TestList(t *testing.T) {
	local, realRun := time.Local, run
	t.Cleanup(func() { time.Local, run = local, realRun })
	time.Local = time.UTC // the fixture's timestamps are in UTC
	var calls [][]string
	run = func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		switch {
		case strings.Contains(strings.Join(args, " "), "list-units"):
			return []byte(fakeUnits), nil
		case strings.HasSuffix(args[len(args)-1], ".timer"):
			return []byte(fakeTimers), nil
		default:
			return []byte(fakeServices), nil
		}
	}
	timers, err := list(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(timers) != 3 || !strings.Contains(strings.Join(calls[0], " "), "--user") {
		t.Fatalf("timers = %+v, calls = %q", timers, calls)
	}
	anacron, masked, fstrim := timers[0], timers[1], timers[2]
	if anacron.Name != "anacron.timer" || !anacron.Active || anacron.Service != "anacron.service" ||
		len(anacron.Calendar) != 1 || anacron.Calendar[0] != "*-*-* 07..23:30:00" ||
		anacron.Result != "success" || anacron.Command != "/usr/sbin/anacron -d -q $ANACRON_ARGS" ||
		anacron.NextRun == nil || anacron.NextRun.Format(time.RFC3339) != "2026-10-05T12:30:03Z" ||
		anacron.LastRun == nil || anacron.LastRun.Format(time.RFC3339) != "2026-10-05T11:32:14Z" {
		t.Fatalf("anacron = %+v", anacron)
	}
	if masked.Active || masked.LastRun != nil || masked.Result != "" || masked.ExitStatus != nil {
		t.Fatalf("masked timer = %+v", masked)
	}
	if fstrim.Result != "exit-code" || fstrim.ExitStatus == nil || *fstrim.ExitStatus != 32 ||
		len(fstrim.Monotonic) != 1 || fstrim.Monotonic[0] != "OnUnitActiveSec=1w" {
		t.Fatalf("fstrim = %+v", fstrim)
	}
}
