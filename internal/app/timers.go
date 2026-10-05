package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yeboahd24/cronwatch/internal/systemd"
)

func timersCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("timers", "cronwatch timers [--all] [--json]",
		"List systemd timers (system and, if available, your user's) with their schedule, last\n"+
			"result and next run, read-only. CRON is the equivalent cron expression where one exists:\n"+
			"wrap the service's command with \"cronwatch run --schedule CRON\" to monitor it like a cron job.")
	all := fs.Bool("all", false, "include inactive and masked timers")
	asJSON := fs.Bool("json", false, "print a JSON array instead of a table")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("timers takes no arguments")
	}
	var timers []systemd.Timer
	for _, scope := range []string{"system", "user"} {
		list, err := systemd.List(ctx, scope)
		switch {
		case errors.Is(err, systemd.ErrNoSystemd):
			return err
		case err != nil && scope == "user":
			// No user session bus, as under cron or ssh without lingering.
			if strings.Contains(err.Error(), "connect to bus") {
				fmt.Fprintln(stderr, "cronwatch: user timers not listed: no user systemd session here")
			} else {
				fmt.Fprintf(stderr, "cronwatch: user timers not listed: %v\n", err)
			}
		case err != nil:
			return err
		}
		for _, t := range list {
			if *all || t.Active {
				timers = append(timers, t)
			}
		}
	}
	if *asJSON {
		out := make([]jsonTimer, 0, len(timers))
		for _, t := range timers {
			out = append(out, newJSONTimer(t))
		}
		return writeJSON(stdout, out)
	}
	if len(timers) == 0 {
		fmt.Fprintln(stdout, "No active systemd timers.")
		return nil
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TIMER\tSCHEDULE\tCRON\tLAST RUN\tRESULT\tNEXT RUN")
	for _, t := range timers {
		name := t.Name
		if t.Scope == "user" {
			name += " (user)"
		}
		cron, _ := timerCron(t)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", name, timerSchedule(t), orDash(cron), timeOrDash(t.LastRun), timerResult(t), timeOrDash(t.NextRun))
	}
	return w.Flush()
}

// timerSchedule describes when a timer fires: its calendar expressions, or
// its monotonic ones such as "OnUnitActiveSec=1d".
func timerSchedule(t systemd.Timer) string {
	if s := append(append([]string{}, t.Calendar...), t.Monotonic...); len(s) > 0 {
		return strings.Join(s, "; ")
	}
	return "—"
}

// timerCron is the cron equivalent of a timer with exactly one calendar
// expression and no monotonic ones.
func timerCron(t systemd.Timer) (string, bool) {
	if len(t.Calendar) != 1 || len(t.Monotonic) != 0 {
		return "", false
	}
	return systemd.CalendarToCron(t.Calendar[0])
}

func timerResult(t systemd.Timer) string {
	switch {
	case t.Result == "":
		return "never run"
	case t.Result == "success":
		return "success"
	case t.ExitStatus != nil && *t.ExitStatus != 0:
		return fmt.Sprintf("%s (exit %d)", t.Result, *t.ExitStatus)
	}
	return t.Result
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

type jsonTimer struct {
	Name        string     `json:"name"`
	Scope       string     `json:"scope"`
	Description string     `json:"description"`
	Service     string     `json:"service"`
	Command     string     `json:"command"`
	Calendar    []string   `json:"calendar"`
	Monotonic   []string   `json:"monotonic"`
	Cron        *string    `json:"cron"`
	Active      bool       `json:"active"`
	LastRun     *time.Time `json:"last_run"`
	NextRun     *time.Time `json:"next_run"`
	Result      string     `json:"result"`
	ExitStatus  *int       `json:"exit_status"`
}

func newJSONTimer(t systemd.Timer) jsonTimer {
	out := jsonTimer{Name: t.Name, Scope: t.Scope, Description: t.Description, Service: t.Service, Command: t.Command,
		Calendar: nonNil(t.Calendar), Monotonic: nonNil(t.Monotonic), Active: t.Active, Result: t.Result, ExitStatus: t.ExitStatus}
	if c, ok := timerCron(t); ok {
		out.Cron = &c
	}
	for _, p := range []struct{ src, dst **time.Time }{{&t.LastRun, &out.LastRun}, {&t.NextRun, &out.NextRun}} {
		if *p.src != nil {
			u := (*p.src).UTC()
			*p.dst = &u
		}
	}
	return out
}
