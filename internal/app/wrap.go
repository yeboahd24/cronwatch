package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yeboahd24/cronwatch/internal/crontab"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// wrapProposal is a crontab with some unmonitored lines wrapped with
// "cronwatch run".
type wrapProposal struct {
	Before, After string
	Wrapped       []wrappedLine
	Skipped       []string // "line N: why", for lines that were asked for or unmonitored
}

type wrappedLine struct {
	Line          int
	Name          string
	Before, After string
}

// braced matches ${NAME}.
var braced = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// cronDefaultPath is the PATH cron gives jobs when the crontab sets none.
const cronDefaultPath = "/usr/bin:/bin"

// proposeWrap wraps the unmonitored lines of text, or only those in lines if
// it is not empty, with "cronwatch run --name NAME". cronwatch is the
// command to call it by; "" picks one (see cronwatchCommand). Names that a
// crontab line or an existing job already uses get a number added.
func proposeWrap(ctx context.Context, s *storage.Store, text string, lines []int, cronwatch string) (wrapProposal, error) {
	p := wrapProposal{Before: text}
	tab := crontab.Parse(text)
	env := shellEnv(tab.Env)
	if cronwatch == "" {
		cronwatch = cronwatchCommand(tab, env)
	}
	// Slugs in use: by lines that already run through cronwatch, and by
	// existing jobs, whose history a new job must not join.
	taken := map[string]bool{}
	for _, e := range tab.Entries {
		if args, found, _ := crontab.FindRun(crontab.Split(e.Command, env)); found {
			if opts, err := parseRunArgs(args, io.Discard); err == nil {
				taken[opts.Spec.Slug] = true
			}
		}
	}
	jobs, err := s.ListJobs(ctx)
	if err != nil {
		return p, err
	}
	for _, j := range jobs {
		taken[j.Slug] = true
	}

	byLine := map[int]crontab.Entry{}
	for _, e := range tab.Entries {
		byLine[e.Line] = e
	}
	for _, n := range lines {
		if _, ok := byLine[n]; !ok {
			p.Skipped = append(p.Skipped, fmt.Sprintf("line %d: not a scheduled command", n))
		}
	}
	textLines := strings.SplitAfter(text, "\n")
	for _, e := range tab.Entries {
		if len(lines) > 0 && !slices.Contains(lines, e.Line) {
			continue
		}
		if crontab.MentionsCronwatch(crontab.Split(e.Command, env)) {
			if len(lines) > 0 {
				p.Skipped = append(p.Skipped, fmt.Sprintf("line %d: already uses cronwatch", e.Line))
			}
			continue
		}
		base := crontab.SuggestName(e.Command, env)
		if base == "" || slugify(base) == "" {
			base = fmt.Sprintf("line %d", e.Line)
		}
		name := base
		for i := 2; taken[slugify(name)]; i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
		command, err := crontab.Wrap(e, env, cronwatch, name)
		if err != nil {
			p.Skipped = append(p.Skipped, fmt.Sprintf("line %d: %v; wrap it by hand", e.Line, err))
			continue
		}
		taken[slugify(name)] = true
		// Replace the command where it ends the line, keeping the schedule
		// and spacing as written.
		old := textLines[e.Line-1]
		body := strings.TrimRight(old, "\r\n")
		at := strings.LastIndex(body, e.Text)
		if at < 0 {
			p.Skipped = append(p.Skipped, fmt.Sprintf("line %d: could not find the command in the line", e.Line))
			continue
		}
		textLines[e.Line-1] = body[:at] + command + old[len(body):]
		p.Wrapped = append(p.Wrapped, wrappedLine{Line: e.Line, Name: name, Before: body, After: body[:at] + command})
	}
	p.After = strings.Join(textLines, "")
	return p, nil
}

// cronwatchCommand is how wrapped lines call cronwatch: as the crontab's
// existing cronwatch lines do, else by name if this binary's directory is on
// the crontab's PATH, else by this binary's absolute path.
func cronwatchCommand(tab crontab.Crontab, env map[string]string) string {
	// Splitting with each variable standing for itself gives the words as
	// written, such as $CW, which line up with the expanded ones.
	literal := map[string]string{}
	for name := range env {
		literal[name] = "${" + name + "}"
	}
	for _, e := range tab.Entries {
		expanded, written := crontab.Split(e.Command, env), crontab.Split(e.Command, literal)
		for i, t := range expanded {
			if t.Operator || t.Dynamic || !crontab.IsCronwatch(t.Text) {
				continue
			}
			if len(written) == len(expanded) && strings.Contains(written[i].Text, "$") {
				word := braced.ReplaceAllString(written[i].Text, "$$$1")
				if strings.ContainsAny(t.Text, " \t") {
					word = `"` + word + `"`
				}
				return word
			}
			return crontab.Quote(t.Text)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return "cronwatch"
	}
	path, ok := tab.Env["PATH"]
	if !ok {
		path = cronDefaultPath
	}
	if filepath.Base(exe) == "cronwatch" && slices.Contains(filepath.SplitList(path), filepath.Dir(exe)) {
		return "cronwatch"
	}
	return crontab.Quote(exe)
}

// lineDiff is a unified diff between two texts that differ only in changed
// lines, as proposeWrap produces, with one line of context.
func lineDiff(before, after, name string) string {
	a, b := strings.Split(strings.TrimSuffix(before, "\n"), "\n"), strings.Split(strings.TrimSuffix(after, "\n"), "\n")
	if len(a) != len(b) {
		return ""
	}
	var changed []int
	for i := range a {
		if a[i] != b[i] {
			changed = append(changed, i)
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s (wrapped)\n", name, name)
	for i := 0; i < len(changed); {
		// Changes whose context would touch share a hunk.
		j := i
		for j+1 < len(changed) && changed[j+1]-changed[j] <= 3 {
			j++
		}
		start, end := max(changed[i]-1, 0), min(changed[j]+2, len(a))
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start+1, end-start, start+1, end-start)
		for k := start; k < end; k++ {
			if a[k] == b[k] {
				fmt.Fprintf(&out, " %s\n", a[k])
			} else {
				fmt.Fprintf(&out, "-%s\n+%s\n", a[k], b[k])
			}
		}
		i = j + 1
	}
	return out.String()
}

// crontabTarget is where sync reads and writes the crontab: the user's
// crontab, or a file.
type crontabTarget struct {
	file string // "" for the user's crontab; "-" for stdin
}

func (t crontabTarget) name() string {
	if t.file == "" {
		return "crontab"
	}
	return t.file
}

func (t crontabTarget) read(ctx context.Context) (string, error) {
	switch t.file {
	case "":
		return readUserCrontab(ctx)
	case "-":
		b, err := io.ReadAll(os.Stdin)
		return string(b), err
	}
	b, err := os.ReadFile(t.file)
	return string(b), err
}

func (t crontabTarget) write(ctx context.Context, text string) error {
	switch t.file {
	case "":
		var errOut bytes.Buffer
		cmd := exec.CommandContext(ctx, "crontab", "-")
		cmd.Stdin, cmd.Stderr = strings.NewReader(text), &errOut
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("crontab -: %w: %s", err, strings.TrimSpace(errOut.String()))
		}
		return nil
	case "-":
		return errors.New("cannot change a crontab read from stdin; pass a file or use your crontab")
	}
	info, err := os.Stat(t.file)
	if err != nil {
		return err
	}
	return os.WriteFile(t.file, []byte(text), info.Mode().Perm())
}

// Crontab backups are exact copies, kept in the data directory because they
// may hold secrets. Each crontab has its own, so a backup of a file is never
// restored as your crontab. Their names sort by time.
const backupSuffix = ".crontab"

// backupDir is where the target's backups are kept.
func (t crontabTarget) backupDir(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "crontab-backups")
	if t.file == "" {
		return filepath.Join(dir, "user"), nil
	}
	abs, err := filepath.Abs(t.file)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(dir, "file-"+hex.EncodeToString(sum[:6])), nil
}

// backupCrontab saves text and returns the backup's name.
func backupCrontab(dir, text string, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := now.UTC().Format("20060102T150405Z")
	for i := 1; ; i++ {
		name := stamp
		if i > 1 {
			name += "-" + strconv.Itoa(i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name+backupSuffix), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.WriteString(text)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return name, err
	}
}

// listBackups returns the names of the backups in dir, oldest first.
func listBackups(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), backupSuffix); ok && !e.IsDir() {
			names = append(names, name)
		}
	}
	// "T090313Z" sorts before "T090313Z-2", so plain order is time order.
	slices.Sort(names)
	return names, nil
}

// readBackup returns the name and content of the named backup in dir, or of
// the newest for "latest".
func readBackup(dir, name string) (string, string, error) {
	names, err := listBackups(dir)
	if err != nil {
		return "", "", err
	}
	if name == "latest" {
		if len(names) == 0 {
			return "", "", errors.New("there are no backups of this crontab")
		}
		name = names[len(names)-1]
	}
	name = strings.TrimSuffix(name, backupSuffix)
	if !slices.Contains(names, name) {
		return "", "", fmt.Errorf("no backup of this crontab named %q (see cronwatch sync --backups)", name)
	}
	b, err := os.ReadFile(filepath.Join(dir, name+backupSuffix))
	return name, string(b), err
}

// replaceCrontab backs up the target's current crontab, writes text in its
// place and registers its jobs. It returns the backup's name.
func replaceCrontab(ctx context.Context, s *storage.Store, target crontabTarget, current, text string) (string, syncResult, error) {
	dir, err := target.backupDir(s.DataDir)
	if err != nil {
		return "", syncResult{}, err
	}
	backup, err := backupCrontab(dir, current, time.Now())
	if err != nil {
		return "", syncResult{}, fmt.Errorf("back up the crontab: %w", err)
	}
	if err := target.write(ctx, text); err != nil {
		return backup, syncResult{}, err
	}
	sync := syncCrontab
	if target.file == "" {
		sync = syncUserCrontab
	}
	result, err := sync(ctx, s, text)
	return backup, result, err
}

// parseLines parses "3,7".
func parseLines(v string) ([]int, error) {
	if v == "" {
		return nil, nil
	}
	var lines []int
	for f := range strings.SplitSeq(v, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--lines: %q is not a line number", f)
		}
		lines = append(lines, n)
	}
	return lines, nil
}

func printWrap(w io.Writer, p wrapProposal, target crontabTarget, applied bool) {
	if len(p.Wrapped) == 0 {
		fmt.Fprintln(w, "No lines to wrap.")
	} else {
		verb := "Would wrap"
		if applied {
			verb = "Wrapped"
		}
		fmt.Fprintf(w, "%s %s with cronwatch run:\n\n", verb, plural(len(p.Wrapped), "line", "lines"))
		for _, l := range p.Wrapped {
			fmt.Fprintf(w, "  line %d: %s\n", l.Line, l.Name)
		}
		fmt.Fprintf(w, "\n%s", lineDiff(p.Before, p.After, target.name()))
	}
	if len(p.Skipped) > 0 {
		fmt.Fprintf(w, "\nNot wrapped:\n")
		for _, s := range p.Skipped {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}
