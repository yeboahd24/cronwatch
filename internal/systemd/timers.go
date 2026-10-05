// Package systemd lists systemd timers and the services they start, read-only.
package systemd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Timer is a systemd timer and the last run of the service it starts.
type Timer struct {
	Scope       string // "system" or "user"
	Name        string // e.g. "apt-daily.timer"
	Description string
	Service     string   // the unit it starts
	Calendar    []string // OnCalendar expressions
	Monotonic   []string // e.g. "OnUnitActiveSec=1d"
	Active      bool     // loaded and active, not masked or stopped
	LastRun     *time.Time
	NextRun     *time.Time
	Result      string // the service's last result: success, exit-code, signal, …; "" if it never ran
	ExitStatus  *int
	Command     string // the service's ExecStart command line
}

// run executes systemctl; tests replace it.
var run = func(ctx context.Context, args ...string) ([]byte, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.Bytes(), nil
}

// ErrNoSystemd means systemctl is not installed.
var ErrNoSystemd = errors.New("systemctl not found: this system does not use systemd")

// List returns the timers of scope "system" or "user", sorted by name.
func List(ctx context.Context, scope string) ([]Timer, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, ErrNoSystemd
	}
	return list(ctx, scope)
}

func list(ctx context.Context, scope string) ([]Timer, error) {
	base := []string{"--no-pager"}
	if scope == "user" {
		base = append(base, "--user")
	}
	units, err := run(ctx, append(base, "list-units", "--type=timer", "--all", "--plain", "--no-legend")...)
	if err != nil {
		return nil, err
	}
	var names []string
	for line := range strings.SplitSeq(string(units), "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasSuffix(f[0], ".timer") {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	props := []string{"--timestamp=unix", "show", "-p", "Id,Description,Unit,TimersCalendar,TimersMonotonic,NextElapseUSecRealtime,LastTriggerUSec,LoadState,ActiveState"}
	out, err := run(ctx, append(append(base, props...), names...)...)
	if err != nil {
		return nil, err
	}
	var timers []Timer
	var services []string
	for _, p := range parseShow(out) {
		t := Timer{Scope: scope, Name: p["Id"], Description: p["Description"], Service: p["Unit"],
			Active:  p["LoadState"] == "loaded" && p["ActiveState"] == "active",
			LastRun: parseTime(p["LastTriggerUSec"]), NextRun: parseTime(p["NextElapseUSecRealtime"])}
		for _, m := range timerSpec.FindAllStringSubmatch(p["TimersCalendar"], -1) {
			t.Calendar = append(t.Calendar, strings.TrimSpace(m[2]))
		}
		for _, m := range timerSpec.FindAllStringSubmatch(p["TimersMonotonic"], -1) {
			t.Monotonic = append(t.Monotonic, m[1]+"="+strings.TrimSpace(m[2]))
		}
		timers = append(timers, t)
		if t.Service != "" {
			services = append(services, t.Service)
		}
	}
	if len(services) > 0 {
		out, err := run(ctx, append(append(base, "--timestamp=unix", "show", "-p", "Id,Result,ExecMainStatus,ExecMainStartTimestamp,ExecStart"), services...)...)
		if err != nil {
			return nil, err
		}
		byID := map[string]map[string]string{}
		for _, p := range parseShow(out) {
			byID[p["Id"]] = p
		}
		for i := range timers {
			p := byID[timers[i].Service]
			if p == nil {
				continue
			}
			if p["ExecMainStartTimestamp"] != "" {
				timers[i].Result = p["Result"]
				if n, err := strconv.Atoi(p["ExecMainStatus"]); err == nil {
					timers[i].ExitStatus = &n
				}
			}
			if m := argv.FindStringSubmatch(p["ExecStart"]); m != nil {
				timers[i].Command = strings.TrimSpace(m[1])
			}
		}
	}
	return timers, nil
}

var (
	// timerSpec matches "{ OnCalendar=*-*-* 06:00:00 ; next_elapse=... }".
	timerSpec = regexp.MustCompile(`\{ (On[A-Za-z]+)=([^;]*);`)
	argv      = regexp.MustCompile(`argv\[\]=([^;]*);`)
)

// parseShow splits "systemctl show" output for several units, which are
// separated by blank lines, into one property map per unit.
func parseShow(out []byte) []map[string]string {
	var units []map[string]string
	current := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				units = append(units, current)
				current = map[string]string{}
			}
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			current[k] = v
		}
	}
	if len(current) > 0 {
		units = append(units, current)
	}
	return units
}

// parseTime parses a systemctl timestamp: "@1759667403" or, for properties
// that ignore --timestamp=unix, "Mon 2026-10-05 12:30:03 GMT" in this
// machine's time zone. Empty, "n/a" and 0 mean never.
func parseTime(v string) *time.Time {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "@") {
		n, err := strconv.ParseInt(v[1:], 10, 64)
		if err != nil || n == 0 {
			return nil
		}
		t := time.Unix(n, 0)
		return &t
	}
	t, err := time.ParseInLocation("Mon 2006-01-02 15:04:05 MST", v, time.Local)
	if err != nil {
		return nil
	}
	return &t
}
