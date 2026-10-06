package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yeboahd24/cronwatch/internal/config"
	"github.com/yeboahd24/cronwatch/internal/crontab"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// Levels of a doctor finding.
const (
	findingOK = iota
	findingInfo
	findingWarning
	findingProblem
)

var findingMarks = [...]string{"✓", "·", "!", "✗"}

// finding is one line of the doctor's report, with an optional hint on how
// to act on it.
type finding struct {
	level      int
	text, hint string
}

type doctorSection struct {
	title    string
	findings []finding
}

func (s *doctorSection) add(level int, hint, format string, args ...any) {
	s.findings = append(s.findings, finding{level: level, text: fmt.Sprintf(format, args...), hint: hint})
}

// process is a running process, as doctor sees it.
type process struct {
	PID  int
	Argv []string
	Env  []string // empty if it cannot be read
}

// listProcesses returns the running processes, and false where they cannot
// be listed. Tests replace it.
var listProcesses = func() ([]process, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, false
	}
	var out []process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue
		}
		p := process{PID: pid, Argv: strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")}
		if env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ")); err == nil {
			p.Env = strings.Split(strings.TrimRight(string(env), "\x00"), "\x00")
		}
		out = append(out, p)
	}
	return out, true
}

// cronDaemons are the names of the programs that run crontabs.
var cronDaemons = []string{"cron", "crond", "cronie", "fcron", "dcron", "busybox-crond"}

// maintenanceStale is how old the last missed-run check may be before doctor
// says checks are not running: serve checks every minute.
const maintenanceStale = 5 * time.Minute

func doctorCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("doctor", "cronwatch doctor [--data-dir DIR]",
		"Check CronWatch's setup: the user, the data directory and database, the crontab's cronwatch\n"+
			"lines, and whether missed runs are being checked. Exits 1 if it finds a problem.")
	dir := fs.String("data-dir", "", "data directory")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("doctor takes no arguments")
	}
	sections := runDoctor(ctx, *dir, time.Now())
	fmt.Fprintf(stdout, "CronWatch %s doctor\n", Version)
	counts := [4]int{}
	for _, s := range sections {
		fmt.Fprintf(stdout, "\n%s\n", s.title)
		for _, f := range s.findings {
			counts[f.level]++
			fmt.Fprintf(stdout, "  %s %s\n", findingMarks[f.level], f.text)
			if f.hint != "" {
				fmt.Fprintf(stdout, "      %s\n", f.hint)
			}
		}
	}
	fmt.Fprintln(stdout)
	switch {
	case counts[findingProblem]+counts[findingWarning] == 0:
		fmt.Fprintln(stdout, "No problems found.")
	default:
		var parts []string
		if n := counts[findingProblem]; n > 0 {
			parts = append(parts, plural(n, "problem", "problems"))
		}
		if n := counts[findingWarning]; n > 0 {
			parts = append(parts, plural(n, "warning", "warnings"))
		}
		fmt.Fprintln(stdout, strings.Join(parts, ", ")+".")
	}
	if counts[findingProblem] > 0 {
		return &ExitError{Code: 1, Err: errors.New("doctor found problems")}
	}
	return nil
}

// runDoctor checks the setup. It never creates the data directory and does
// not check for missed runs itself, so it reports what it finds.
func runDoctor(ctx context.Context, dirFlag string, now time.Time) []doctorSection {
	var sections []doctorSection

	who := doctorSection{title: "User"}
	if u, err := user.Current(); err != nil {
		who.add(findingWarning, "", "Could not tell which user this is: %v", err)
	} else {
		who.add(findingOK, "", "Running as %s (uid %s), home %s", u.Username, u.Uid, u.HomeDir)
		if u.Uid == "0" {
			who.add(findingWarning, "Run cronwatch as the user whose crontab runs the jobs, or pass the same --data-dir.",
				"Running as root: this reads root's data directory, not the one jobs in your own crontab record to")
		}
	}
	sections = append(sections, who)

	data := doctorSection{title: "Data"}
	dir, source := dirFlag, "--data-dir"
	if dir == "" {
		if v := os.Getenv("CRONWATCH_DATA_DIR"); v != "" {
			dir, source = v, "$CRONWATCH_DATA_DIR"
		} else if cfg, err := config.Load(); err == nil {
			dir, source = cfg.DataDir, "the default"
		} else {
			data.add(findingProblem, "Set CRONWATCH_DATA_DIR or pass --data-dir.", "Could not find the data directory: %v", err)
			return append(sections, data)
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	var s *storage.Store
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		data.add(findingInfo, "The first cronwatch run, ping or sync creates it.", "Data directory %s (%s) does not exist yet", dir, source)
	case err != nil:
		data.add(findingProblem, "", "Data directory %s (%s): %v", dir, source, err)
	case !info.IsDir():
		data.add(findingProblem, "", "Data directory %s (%s) is not a directory", dir, source)
	default:
		data.add(findingOK, "", "Data directory %s (%s)", dir, source)
		checkOwnership(&data, dir, info, 0o077, "Run chmod 700 on it: runs and crontab backups may hold secrets.")
		checkFreeSpace(&data, dir)
		s = openForDoctor(ctx, &data, dir)
	}
	if s != nil {
		defer s.Close()
	}
	sections = append(sections, data)
	sections = append(sections, doctorCrontab(ctx, dir))
	if s != nil {
		sections = append(sections, doctorMonitoring(ctx, s, dir, now), doctorJobs(ctx, s, now))
	}
	return sections
}

// checkOwnership reports who owns path and whether its mode lets anyone
// else in, given the bits that should be clear.
func checkOwnership(sec *doctorSection, path string, info fs.FileInfo, othersBits fs.FileMode, hint string) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if ok && int(st.Uid) != os.Getuid() {
		sec.add(findingProblem, "Run cronwatch as the owner, or chown the data directory to this user.",
			"%s is owned by uid %d, not this user (uid %d)", filepath.Base(path), st.Uid, os.Getuid())
	}
	if mode := info.Mode().Perm(); mode&othersBits != 0 {
		sec.add(findingWarning, hint, "%s has mode %04o; other users can read it", filepath.Base(path), mode)
	}
}

// checkFreeSpace warns when the data directory's file system is nearly full.
func checkFreeSpace(sec *doctorSection, dir string) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return
	}
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if free < 200<<20 {
		sec.add(findingWarning, "Runs fail to record when the disk is full; free space or prune old runs.",
			"Only %s free on the data directory's disk", humanBytes(int64(free)))
	}
}

// openForDoctor opens and checks the database, without checking for missed
// runs as the listing commands do.
func openForDoctor(ctx context.Context, sec *doctorSection, dir string) *storage.Store {
	path := filepath.Join(dir, "cronwatch.db")
	if info, err := os.Stat(path); err == nil {
		checkOwnership(sec, path, info, 0o077, "Run chmod 600 on it: runs may hold secrets.")
	}
	s, err := storage.Open(ctx, dir)
	if err != nil {
		sec.add(findingProblem, "Runs cannot be recorded until this is fixed.", "Could not open the database: %v", err)
		return nil
	}
	var check string
	if err := s.DB.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		if err != nil {
			check = err.Error()
		}
		sec.add(findingProblem, "Restore it from a backup, or move it aside to start afresh.", "The database is damaged: %s", check)
	}
	var jobs, runs int
	var oldest *string
	_ = s.DB.QueryRowContext(ctx, "SELECT count(*) FROM jobs").Scan(&jobs)
	_ = s.DB.QueryRowContext(ctx, "SELECT count(*), min(started_at) FROM runs").Scan(&runs, &oldest)
	size := int64(0)
	for _, suffix := range []string{"", "-wal"} {
		if info, err := os.Stat(path + suffix); err == nil {
			size += info.Size()
		}
	}
	text := fmt.Sprintf("Database %s: %s, %s, %s", path, humanBytes(size), plural(jobs, "job", "jobs"), plural(runs, "run", "runs"))
	if oldest != nil && len(*oldest) >= 10 {
		text += ", the oldest from " + (*oldest)[:10]
	}
	level, hint := findingOK, ""
	if size > 1<<30 {
		level, hint = findingWarning, "cronwatch prune --keep N or --older-than DURATION keeps it smaller."
	}
	sec.add(level, hint, "%s", text)
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// doctorCrontab checks the user's crontab: that its cronwatch lines can find
// cronwatch and record where this doctor reads, and what is not monitored.
func doctorCrontab(ctx context.Context, dir string) doctorSection {
	sec := doctorSection{title: "Crontab"}
	text, err := readUserCrontab(ctx)
	switch {
	case errors.Is(err, errNoCrontabCommand):
		sec.add(findingInfo, "", "There is no crontab command, so there is no crontab to check")
		return sec
	case err != nil:
		sec.add(findingWarning, "", "Could not read the crontab: %v", err)
		return sec
	}
	tab := crontab.Parse(text)
	if len(tab.Entries) == 0 {
		sec.add(findingInfo, "Add a line such as: 0 2 * * * cronwatch run --name Backup -- /usr/local/bin/backup.sh",
			"The crontab has no scheduled lines")
		return sec
	}
	env := shellEnv(tab.Env)
	path, ok := tab.Env["PATH"]
	if !ok {
		path = cronDefaultPath
	}
	// Where cronwatch records under cron, when a line does not say.
	cronDir := tab.Env["CRONWATCH_DATA_DIR"]
	if cronDir == "" {
		base := tab.Env["XDG_CONFIG_HOME"]
		if base == "" {
			base = filepath.Join(env["HOME"], ".config")
		}
		cronDir = filepath.Join(base, "cronwatch")
	}
	monitored, unmonitored := 0, []crontab.Entry{}
	// Problems with lines are reported after the summary of the crontab.
	lines := doctorSection{}
	for _, e := range tab.Entries {
		tokens := crontab.Split(e.Command, env)
		i := slices.IndexFunc(tokens, func(t crontab.Token) bool { return !t.Operator && crontab.IsCronwatch(t.Text) })
		if i < 0 {
			unmonitored = append(unmonitored, e)
			continue
		}
		if _, found, _ := crontab.FindRun(tokens); found {
			monitored++
		}
		if tokens[i].Dynamic {
			continue
		}
		if where, ok := findCronwatch(tokens[i].Text, path); !ok {
			lines.add(findingProblem, fmt.Sprintf("Use its full path, or add its directory to PATH at the top of the crontab (PATH is %s).", path),
				"Line %d: cron cannot find %s", e.Line, where)
		}
		lineDir := cronDir
		for j := i + 1; j < len(tokens) && !tokens[j].Operator; j++ {
			switch t := tokens[j].Text; {
			case (t == "--data-dir" || t == "-data-dir") && j+1 < len(tokens):
				lineDir = tokens[j+1].Text
			case strings.HasPrefix(t, "--data-dir="), strings.HasPrefix(t, "-data-dir="):
				_, lineDir, _ = strings.Cut(t, "=")
			}
		}
		if abs, err := filepath.Abs(lineDir); err == nil && abs != dir {
			lines.add(findingProblem, "Set CRONWATCH_DATA_DIR in the crontab to match, or pass the same --data-dir to cronwatch commands.",
				"Line %d records to %s, not %s, which this doctor (and serve, run the same way) reads", e.Line, abs, dir)
		}
	}
	sec.add(findingOK, "", "%s, %d run through cronwatch run", plural(len(tab.Entries), "scheduled line", "scheduled lines"), monitored)
	sec.findings = append(sec.findings, lines.findings...)
	if n := len(unmonitored); n > 0 {
		numbers := make([]string, 0, n)
		for _, e := range unmonitored {
			numbers = append(numbers, strconv.Itoa(e.Line))
		}
		sec.add(findingWarning, "cronwatch sync --wrap shows how to wrap them.", "%s not monitored: %s %s",
			plural(n, "line is", "lines are"), map[bool]string{true: "line", false: "lines"}[n == 1], strings.Join(numbers, ", "))
	}
	return sec
}

// findCronwatch reports where cron would find the cronwatch command word,
// given cron's PATH, or the word itself if it would not.
func findCronwatch(word, path string) (string, bool) {
	executable := func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
	}
	if strings.Contains(word, "/") {
		return word, executable(word)
	}
	for _, d := range filepath.SplitList(path) {
		if p := filepath.Join(d, word); executable(p) {
			return p, true
		}
	}
	return word + " on PATH", false
}

// doctorMonitoring reports whether missed runs are being checked: by a
// running cronwatch serve, or recently by another command.
func doctorMonitoring(ctx context.Context, s *storage.Store, dir string, now time.Time) doctorSection {
	sec := doctorSection{title: "Missed-run detection"}
	procs, listed := listProcesses()
	var serves []int
	daemon := false
	for _, p := range procs {
		if len(p.Argv) == 0 {
			continue
		}
		if slices.Contains(cronDaemons, filepath.Base(p.Argv[0])) {
			daemon = true
		}
		i := slices.IndexFunc(p.Argv, crontab.IsCronwatch)
		if i < 0 || i+1 >= len(p.Argv) || p.Argv[i+1] != "serve" || serveDataDir(p) != dir {
			continue
		}
		serves = append(serves, p.PID)
	}
	if listed && !daemon {
		sec.add(findingWarning, "Start the cron service, such as with systemctl start cron.", "No cron daemon is running, so crontab jobs do not run")
	}
	if len(serves) > 0 {
		sec.add(findingOK, "", "cronwatch serve is running for this data directory (pid %s)", joinInts(serves))
	}
	views, err := s.ListJobViews(ctx, now)
	if err != nil {
		sec.add(findingWarning, "", "Could not read the jobs: %v", err)
		return sec
	}
	scheduled := 0
	for _, v := range views {
		if v.Schedule != nil && v.Monitored(now) {
			scheduled++
		}
	}
	last, err := s.Maintained(ctx)
	if err != nil {
		sec.add(findingWarning, "", "Could not read when missed runs were last checked: %v", err)
		return sec
	}
	const howTo = "Run cronwatch serve (for example as a systemd user service), or schedule: * * * * * cronwatch check >/dev/null"
	switch {
	case scheduled == 0:
		sec.add(findingInfo, "", "No monitored job has a schedule, so there are no runs to miss")
	case last == nil:
		sec.add(findingProblem, howTo, "Missed runs have never been checked, so they are not reported")
	case now.Sub(*last) > maintenanceStale:
		sec.add(findingWarning, howTo, "Missed runs were last checked %s ago, so new ones are not being reported", shortDuration(now.Sub(*last).Round(time.Minute)))
	default:
		sec.add(findingOK, "", "Missed runs were last checked %s ago", shortDuration(now.Sub(*last).Round(time.Second)))
	}
	return sec
}

// serveDataDir is the data directory a cronwatch serve process uses, as far
// as its arguments and environment tell.
func serveDataDir(p process) string {
	for i, a := range p.Argv {
		switch {
		case (a == "--data-dir" || a == "-data-dir") && i+1 < len(p.Argv):
			return absPath(p.Argv[i+1])
		case strings.HasPrefix(a, "--data-dir="), strings.HasPrefix(a, "-data-dir="):
			_, v, _ := strings.Cut(a, "=")
			return absPath(v)
		}
	}
	env := map[string]string{}
	for _, kv := range p.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	if v := env["CRONWATCH_DATA_DIR"]; v != "" {
		return absPath(v)
	}
	base := env["XDG_CONFIG_HOME"]
	if base == "" {
		home := env["HOME"]
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "cronwatch")
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// doctorJobs reports jobs that are not fully monitored and alerts that
// could not be delivered.
func doctorJobs(ctx context.Context, s *storage.Store, now time.Time) doctorSection {
	sec := doctorSection{title: "Jobs and alerts"}
	views, err := s.ListJobViews(ctx, now)
	if err != nil {
		sec.add(findingWarning, "", "Could not read the jobs: %v", err)
		return sec
	}
	var paused, archived, unscheduled, hooked []string
	byID := map[string]model.JobView{}
	for _, v := range views {
		byID[v.ID] = v
		switch {
		case v.ArchivedAt != nil:
			archived = append(archived, v.Name)
		case v.Paused(now):
			paused = append(paused, v.Name)
		case v.Schedule == nil:
			unscheduled = append(unscheduled, v.Name)
		}
		if v.OnFailure != "" || v.OnRecover != "" {
			hooked = append(hooked, v.Name)
		}
	}
	if len(views) == 0 {
		sec.add(findingInfo, "Wrap a crontab line with cronwatch run, or run cronwatch sync --wrap.", "No jobs are registered yet")
		return sec
	}
	sec.add(findingOK, "", "%s, %d with an --on-failure or --on-recover hook", plural(len(views), "job", "jobs"), len(hooked))
	for _, g := range []struct {
		names      []string
		what, hint string
	}{
		{paused, "paused", "cronwatch resume SLUG monitors one again."},
		{archived, "archived", ""},
		{unscheduled, "without a schedule, so not checked for missed runs", "Pass --schedule, or run the job from your crontab and cronwatch sync."},
	} {
		if len(g.names) > 0 {
			sec.add(findingInfo, g.hint, "%s %s: %s", plural(len(g.names), "job", "jobs"), g.what, strings.Join(g.names, ", "))
		}
	}
	undelivered, err := s.UndeliveredAlerts(ctx)
	if err != nil {
		sec.add(findingWarning, "", "Could not read the alerts: %v", err)
		return sec
	}
	if len(undelivered) > 0 {
		var names []string
		total := 0
		for id, n := range undelivered {
			total += n
			names = append(names, byID[id].Name)
		}
		slices.Sort(names)
		sec.add(findingWarning, "Each job's page shows the hook's error and output.", "%s could not be delivered: %s",
			plural(total, "alert", "alerts"), strings.Join(names, ", "))
	}
	return sec
}
