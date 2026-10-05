package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/crontab"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// secretName matches variable names whose values are not stored in crontab
// history.
var (
	secretName = regexp.MustCompile(`(?i)(key|token|secret|pass|pwd|auth|credential|cookie|session)`)
	assignment = regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)(\s*=\s*)(.*)$`)
)

// maskCrontab replaces the values of secret-looking variable assignments
// with a short hash, so a changed secret is still noticed but never stored.
func maskCrontab(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		m := assignment.FindStringSubmatch(line)
		if m == nil || !secretName.MatchString(m[2]) || m[4] == "" {
			continue
		}
		sum := sha256.Sum256([]byte(m[4]))
		lines[i] = m[1] + m[2] + m[3] + "‹hidden " + hex.EncodeToString(sum[:4]) + "›"
	}
	return strings.Join(lines, "\n")
}

// jobLine is a crontab line that runs a job through cronwatch.
type jobLine struct {
	Schedule string // as written, e.g. "0 2 * * *" or "@daily"
	Text     string // the whole line
}

// crontabLines splits a crontab into the lines that run jobs, by slug, and
// every other meaningful line (assignments and unmonitored jobs).
func crontabLines(text string) (jobs map[string]jobLine, other []string) {
	jobs = map[string]jobLine{}
	raw := strings.Split(text, "\n")
	tab := crontab.Parse(text)
	env := shellEnv(tab.Env)
	isJob := map[int]bool{}
	for _, entry := range tab.Entries {
		args, found, dynamic := crontab.FindRun(crontab.Split(entry.Command, env))
		if !found || dynamic {
			continue
		}
		opts, err := parseRunArgs(args, io.Discard)
		if err != nil {
			continue
		}
		if _, dup := jobs[opts.Spec.Slug]; dup {
			continue // sync reports duplicates; the first line is the job's
		}
		jobs[opts.Spec.Slug] = jobLine{Schedule: entry.Raw, Text: strings.TrimSpace(raw[entry.Line-1])}
		isJob[entry.Line] = true
	}
	for i, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") && !isJob[i+1] {
			other = append(other, line)
		}
	}
	return jobs, other
}

// diffCrontabs lists what changed from before to after.
func diffCrontabs(before, after string) []storage.CrontabChange {
	oldJobs, oldOther := crontabLines(before)
	newJobs, newOther := crontabLines(after)
	var changes []storage.CrontabChange
	for slug, now := range newJobs {
		was, ok := oldJobs[slug]
		switch {
		case !ok:
			changes = append(changes, storage.CrontabChange{JobSlug: slug, Kind: "added", After: now.Text})
		case was.Text == now.Text:
		// A schedule repeated in --schedule may change along with it.
		case was.Schedule != now.Schedule && strings.ReplaceAll(was.Text, was.Schedule, now.Schedule) == now.Text:
			changes = append(changes, storage.CrontabChange{JobSlug: slug, Kind: "schedule", Before: was.Schedule, After: now.Schedule})
		default:
			changes = append(changes, storage.CrontabChange{JobSlug: slug, Kind: "changed", Before: was.Text, After: now.Text})
		}
	}
	for slug, was := range oldJobs {
		if _, ok := newJobs[slug]; !ok {
			changes = append(changes, storage.CrontabChange{JobSlug: slug, Kind: "removed", Before: was.Text})
		}
	}
	slices.SortStableFunc(changes, func(a, b storage.CrontabChange) int { return strings.Compare(a.JobSlug, b.JobSlug) })
	added, removed := multisetDiff(oldOther, newOther)
	for _, l := range added {
		changes = append(changes, storage.CrontabChange{Kind: "line_added", After: l})
	}
	for _, l := range removed {
		changes = append(changes, storage.CrontabChange{Kind: "line_removed", Before: l})
	}
	return changes
}

// multisetDiff returns the lines only in after and only in before, in order.
func multisetDiff(before, after []string) (added, removed []string) {
	count := func(lines []string) map[string]int {
		m := map[string]int{}
		for _, l := range lines {
			m[l]++
		}
		return m
	}
	inBefore, inAfter := count(before), count(after)
	for _, l := range after {
		if inBefore[l] > 0 {
			inBefore[l]--
		} else {
			added = append(added, l)
		}
	}
	for _, l := range before {
		if inAfter[l] > 0 {
			inAfter[l]--
		} else {
			removed = append(removed, l)
		}
	}
	return added, removed
}

// recordCrontab stores text as a new snapshot if it differs from the last
// one, with what changed, and returns the changes. The first snapshot is the
// baseline and has none.
func recordCrontab(ctx context.Context, s *storage.Store, text string, now time.Time) ([]storage.CrontabChange, error) {
	masked := maskCrontab(text)
	sum := sha256.Sum256([]byte(masked))
	hash := hex.EncodeToString(sum[:])
	latest, err := s.LatestCrontabSnapshot(ctx)
	if err != nil || latest != nil && latest.Hash == hash {
		return nil, err
	}
	var changes []storage.CrontabChange
	if latest != nil {
		changes = diffCrontabs(latest.Content, masked)
	}
	err = s.RecordCrontabSnapshot(ctx, storage.CrontabSnapshot{TakenAt: now, Hash: hash, Content: masked}, changes)
	return changes, err
}

func crontabHistoryCommand(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlagSet("crontab-history", "cronwatch crontab-history [--limit N] [JOB-SLUG]",
		"List changes to your crontab, newest first, as recorded by \"cronwatch serve\" (every minute)\n"+
			"and \"cronwatch sync\". Values of secret-looking variables are not stored.")
	dir := fs.String("data-dir", "", "data directory")
	limit := fs.Int("limit", 50, "most changes to list")
	if err := parseFlags(fs, args, stdout); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("crontab-history accepts at most one job slug")
	}
	if *limit <= 0 {
		return errors.New("--limit must be positive")
	}
	s, err := openForList(ctx, *dir)
	if err != nil {
		return err
	}
	defer s.Close()
	var changes []storage.CrontabChange
	if fs.NArg() == 1 {
		changes, err = s.JobCrontabChanges(ctx, fs.Arg(0), *limit)
	} else {
		changes, err = s.CrontabChanges(ctx, *limit)
	}
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		if latest, err := s.LatestCrontabSnapshot(ctx); err == nil && latest == nil {
			fmt.Fprintln(stdout, "No crontab recorded yet. Run \"cronwatch sync\" or keep \"cronwatch serve\" running.")
		} else {
			fmt.Fprintln(stdout, "No changes recorded.")
		}
		return nil
	}
	names := map[string]string{}
	if jobs, err := s.ListJobs(ctx); err == nil {
		for _, j := range jobs {
			names[j.Slug] = j.Name
		}
	}
	var last time.Time
	for _, c := range changes {
		if !c.TakenAt.Equal(last) {
			if !last.IsZero() {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintln(stdout, c.TakenAt.Local().Format("2006-01-02 15:04"))
			last = c.TakenAt
		}
		fmt.Fprintf(stdout, "  %s\n", describeChange(c, names))
	}
	return nil
}

// describeChange says what a change was, in one line.
func describeChange(c storage.CrontabChange, names map[string]string) string {
	name := names[c.JobSlug]
	if name == "" {
		name = c.JobSlug
	}
	switch c.Kind {
	case "added":
		return fmt.Sprintf("%s: added to the crontab: %s", name, c.After)
	case "removed":
		return fmt.Sprintf("%s: removed from the crontab (was: %s)", name, c.Before)
	case "schedule":
		return fmt.Sprintf("%s: schedule changed from %s to %s", name, c.Before, c.After)
	case "changed":
		return fmt.Sprintf("%s: line changed\n    - %s\n    + %s", name, c.Before, c.After)
	case "line_added":
		return "+ " + c.After
	case "line_removed":
		return "- " + c.Before
	}
	return c.Kind
}
