// Package crontab parses user crontabs well enough to find the jobs they run.
// It understands schedules, environment assignments and a POSIX-shell subset
// of the command (quotes, escapes, $VAR, ~, operators); command substitution
// is detected but not evaluated.
package crontab

import (
	"bufio"
	"regexp"
	"strings"
)

// Entry is one scheduled line of a crontab.
type Entry struct {
	Line     int    // 1-based line number
	Schedule string // 5-field expression; "" for @reboot
	Raw      string // schedule as written, e.g. "@daily" or "*/5 * * * *"
	Command  string // shell command, with cron's % handling applied
}

// Crontab is a parsed crontab.
type Crontab struct {
	Entries []Entry
	Env     map[string]string // NAME=value assignments, in effect for all lines
}

var (
	envLine     = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$`)
	descriptors = map[string]string{
		"@yearly":   "0 0 1 1 *",
		"@annually": "0 0 1 1 *",
		"@monthly":  "0 0 1 * *",
		"@weekly":   "0 0 * * 0",
		"@daily":    "0 0 * * *",
		"@midnight": "0 0 * * *",
		"@hourly":   "0 * * * *",
		"@reboot":   "",
	}
)

// Parse reads a user crontab (no user field).
func Parse(text string) Crontab {
	c := Crontab{Env: map[string]string{}}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Schedules start with a digit, '*' or '@', so a leading letter is
		// an environment assignment.
		if m := envLine.FindStringSubmatch(line); m != nil {
			c.Env[m[1]] = unquoteEnv(m[2])
			continue
		}
		var raw, schedule, command string
		if strings.HasPrefix(line, "@") {
			word, rest, _ := strings.Cut(line, " ")
			expr, ok := descriptors[strings.ToLower(word)]
			if !ok {
				continue
			}
			raw, schedule, command = word, expr, rest
		} else {
			fields := strings.Fields(line)
			if len(fields) < 6 {
				continue
			}
			schedule = strings.Join(fields[:5], " ")
			raw = schedule
			command = afterFields(line, 5)
		}
		c.Entries = append(c.Entries, Entry{Line: n, Schedule: schedule, Raw: raw, Command: cronPercent(strings.TrimSpace(command))})
	}
	return c
}

func unquoteEnv(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// afterFields returns line after its first n whitespace-separated fields.
func afterFields(line string, n int) string {
	rest := line
	for range n {
		rest = strings.TrimLeft(rest, " \t")
		i := strings.IndexAny(rest, " \t")
		if i < 0 {
			return ""
		}
		rest = rest[i:]
	}
	return rest
}

// cronPercent applies cron's rule that an unescaped % ends the command (the
// rest becomes stdin) and \% is a literal %.
func cronPercent(command string) string {
	var b strings.Builder
	for i := 0; i < len(command); i++ {
		switch {
		case command[i] == '\\' && i+1 < len(command) && command[i+1] == '%':
			b.WriteByte('%')
			i++
		case command[i] == '%':
			return b.String()
		default:
			b.WriteByte(command[i])
		}
	}
	return b.String()
}
