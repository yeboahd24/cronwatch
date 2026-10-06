package crontab

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// ErrStdin means a command uses cron's % to pass input, which cannot be
// wrapped safely: cron splits at the first % anywhere on the line, even
// inside the quotes a wrapper would add.
var ErrStdin = errors.New("uses % to pass input to the command")

// Wrap returns e's command rewritten to run through "cronwatch run --name
// name". cronwatch is the cronwatch command to use, as it should appear in
// the crontab. A simple command is prefixed, so its redirections now apply to
// cronwatch, which passes the output through. Anything else runs with sh -c.
// When only setup commands such as "cd /srv/app &&" come before the main one,
// its redirections are moved outside, so its output still reaches the file
// and cronwatch records it too; otherwise moving them would change where the
// other commands' output goes, so the line is kept whole.
func Wrap(e Entry, env map[string]string, cronwatch, name string) (string, error) {
	if strings.Contains(strings.ReplaceAll(e.Text, `\%`, ""), "%") {
		return "", ErrStdin
	}
	prefix := cronwatch + " run --name " + Quote(name) + " -- "
	tokens := Split(e.Text, env)
	if simple(tokens) {
		return prefix + e.Text, nil
	}
	body, redirects := splitTrailingRedirects(e.Text, env)
	if redirects != "" && !setupThenOne(Split(body, env)) {
		body, redirects = e.Text, ""
	}
	out := prefix + "sh -c " + Quote(body)
	if redirects != "" {
		out += " " + redirects
	}
	return out, nil
}

// simple reports whether tokens are one command whose first word is the
// program: no control operators, and no leading VAR=value assignment, which
// would become cronwatch's command.
func simple(tokens []Token) bool {
	for _, t := range tokens {
		if t.Operator && !isRedirect(t.Text) {
			return false
		}
	}
	for _, t := range tokens {
		if !t.Operator {
			return !assignment.MatchString(t.Text)
		}
	}
	return false
}

// setupThenOne reports whether tokens are setup commands joined by && or ;
// and then one last command, such as "cd /srv/app && ./run.sh".
func setupThenOne(tokens []Token) bool {
	start := 0
	for i, t := range tokens {
		if !t.Operator || isRedirect(t.Text) {
			continue
		}
		if t.Text != "&&" && t.Text != ";" {
			return false
		}
		if i == start || tokens[start].Operator || !slices.Contains(setupCommands, tokens[start].Text) {
			return false
		}
		start = i + 1
	}
	return start > 0 && start < len(tokens)
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

func isRedirect(op string) bool { return strings.ContainsAny(op, "<>") }

// splitTrailingRedirects splits text before the redirections at its end,
// such as ">> /var/log/x.log 2>&1". It returns text unchanged if it does not
// end with any.
func splitTrailingRedirects(text string, env map[string]string) (body, redirects string) {
	whole := Split(text, env)
	for i := 0; i < len(text); i++ {
		if text[i] != ' ' && text[i] != '\t' {
			continue
		}
		tail := Split(text[i:], env)
		if !onlyRedirects(tail) || !slices.Equal(append(Split(text[:i], env), tail...), whole) {
			continue
		}
		return strings.TrimSpace(text[:i]), strings.TrimSpace(text[i:])
	}
	return text, ""
}

// onlyRedirects reports whether tokens are redirections, each with its
// target unless it duplicates a descriptor (2>&1).
func onlyRedirects(tokens []Token) bool {
	if len(tokens) == 0 {
		return false
	}
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if !t.Operator || !isRedirect(t.Text) {
			return false
		}
		if strings.Contains(t.Text, "&") && !strings.HasPrefix(t.Text, "&") {
			continue // 2>&1 needs no target
		}
		if i+1 >= len(tokens) || tokens[i+1].Operator {
			return false
		}
		i++
	}
	return true
}

// Quote returns s as a single shell word, quoted only if it needs to be.
func Quote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:@+,=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Words that start a command without being the program a job is named
// after: setup commands, wrappers and interpreters.
var (
	setupCommands = []string{"cd", "export", "umask", "sleep", "source", ".", "set", "true", ":"}
	wrappers      = []string{"env", "nice", "ionice", "nohup", "timeout", "flock", "sudo", "chronic", "exec", "time", "setsid", "systemd-cat", "caffeinate"}
	interpreters  = []string{"sh", "bash", "zsh", "dash", "python", "python2", "python3", "perl", "ruby", "node", "php", "Rscript"}
	duration      = regexp.MustCompile(`^[0-9][0-9.]*[a-z]*$`)
)

// SuggestName returns a job name for command: the base name, without an
// extension, of the program or script it runs. It returns "" if it finds
// none.
func SuggestName(command string, env map[string]string) string {
	var segments [][]Token
	var cur []Token
	for _, t := range Split(command, env) {
		if t.Operator && !isRedirect(t.Text) {
			segments, cur = append(segments, cur), nil
			continue
		}
		cur = append(cur, t)
	}
	segments = append(segments, cur)
	for _, seg := range segments {
		if name := programName(seg, env); name != "" {
			return name
		}
	}
	return ""
}

// programName names the command tokens run, which hold no control
// operators. It returns "" for a setup command such as cd.
func programName(tokens []Token, env map[string]string) string {
	first, skipPath := true, false
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Operator {
			if !strings.Contains(t.Text, "&") {
				i++ // the redirection's target
			}
			continue
		}
		if t.Dynamic || assignment.MatchString(t.Text) || strings.HasPrefix(t.Text, "-") || duration.MatchString(t.Text) {
			continue
		}
		word := filepath.Base(t.Text)
		if first && slices.Contains(setupCommands, word) {
			return ""
		}
		first = false
		if skipPath { // flock's lock file
			skipPath = false
			continue
		}
		switch {
		case slices.Contains(wrappers, word):
			skipPath = word == "flock"
			continue
		case slices.Contains(interpreters, word) || strings.HasPrefix(word, "python3."):
			// "sh -c 'script'" is named after the script it runs.
			if i+2 < len(tokens) && tokens[i+1].Text == "-c" && !tokens[i+2].Operator {
				return SuggestName(tokens[i+2].Text, env)
			}
			continue
		}
		if ext := filepath.Ext(word); ext != "" && len(ext) <= 4 {
			word = strings.TrimSuffix(word, ext)
		}
		return word
	}
	return ""
}
