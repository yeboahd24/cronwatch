package crontab

import (
	"path/filepath"
	"strings"
)

// Token is a shell word or operator.
type Token struct {
	Text     string
	Operator bool // ; & && | || < > >> 2>&1 ( ) and similar
	Dynamic  bool // contains $(...) or `...`, which is not evaluated
}

// Split tokenizes a POSIX shell command, expanding $NAME, ${NAME} and a
// leading ~ from env. It is a subset: no globbing, aliases or functions.
func Split(command string, env map[string]string) []Token {
	var (
		tokens  []Token
		word    strings.Builder
		inWord  bool
		dynamic bool
	)
	flush := func() {
		if inWord {
			tokens = append(tokens, Token{Text: word.String(), Dynamic: dynamic})
		}
		word.Reset()
		inWord, dynamic = false, false
	}
	expand := func(s string, i int) int { // s[i] == '$'; returns index after
		if i+1 < len(s) && s[i+1] == '(' {
			dynamic = true
			end := matchParen(s, i+1)
			word.WriteString(s[i:end])
			return end
		}
		if i+1 < len(s) && s[i+1] == '{' {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				word.WriteString(env[s[i+2:i+end]])
				return i + end + 1
			}
		}
		j := i + 1
		for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || j > i+1 && s[j] >= '0' && s[j] <= '9') {
			j++
		}
		if j == i+1 {
			word.WriteByte('$')
			return j
		}
		word.WriteString(env[s[i+1:j]])
		return j
	}

	s := command
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
			i++
		case c == '#' && !inWord:
			flush()
			return tokens
		case strings.IndexByte(";&|<>()", c) >= 0:
			// "2>&1" and "2>file": digits directly before a redirect belong to it.
			op := ""
			if c == '<' || c == '>' {
				if w := word.String(); inWord && !dynamic && w != "" && strings.Trim(w, "0123456789") == "" {
					op = w
					word.Reset()
					inWord = false
				}
			}
			flush()
			j := i + 1
			for j < len(s) && strings.IndexByte(";&|<>", s[j]) >= 0 && c != '(' && c != ')' {
				j++
			}
			// Keep fd duplication ("&1") attached to the redirect.
			if s[j-1] == '&' && (c == '<' || c == '>') {
				for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '-') {
					j++
				}
			}
			tokens = append(tokens, Token{Text: op + s[i:j], Operator: true})
			i = j
		case c == '\'':
			inWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				word.WriteString(s[i+1:])
				i = len(s)
			} else {
				word.WriteString(s[i+1 : i+1+end])
				i += end + 2
			}
		case c == '"':
			inWord = true
			i++
			for i < len(s) && s[i] != '"' {
				switch {
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\\\"$`\n", s[i+1]) >= 0:
					word.WriteByte(s[i+1])
					i += 2
				case s[i] == '$':
					i = expand(s, i)
				case s[i] == '`':
					dynamic = true
					word.WriteByte(s[i])
					i++
				default:
					word.WriteByte(s[i])
					i++
				}
			}
			i++ // closing quote
		case c == '\\':
			inWord = true
			if i+1 < len(s) {
				word.WriteByte(s[i+1])
			}
			i += 2
		case c == '$':
			inWord = true
			i = expand(s, i)
		case c == '`':
			inWord = true
			dynamic = true
			word.WriteByte(c)
			i++
		case c == '~' && !inWord:
			inWord = true
			end := i + 1
			for end < len(s) && strings.IndexByte(" \t/;&|<>", s[end]) < 0 {
				end++
			}
			if end == i+1 { // "~" or "~/..." is the current user's home
				word.WriteString(env["HOME"])
			} else { // "~user" is not expanded
				word.WriteString(s[i:end])
			}
			i = end
		default:
			inWord = true
			word.WriteByte(c)
			i++
		}
	}
	flush()
	return tokens
}

func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

// IsCronwatch reports whether a command word invokes the cronwatch binary.
func IsCronwatch(word string) bool {
	return filepath.Base(word) == "cronwatch"
}

// FindRun returns the arguments after "cronwatch run" in tokens, up to the
// next shell operator. found is false when the command does not run
// cronwatch run; dynamic is true when those arguments use command
// substitution and cannot be read statically.
func FindRun(tokens []Token) (args []string, found, dynamic bool) {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i].Operator || !IsCronwatch(tokens[i].Text) || tokens[i+1].Operator || tokens[i+1].Text != "run" {
			continue
		}
		for _, t := range tokens[i+2:] {
			if t.Operator {
				break
			}
			dynamic = dynamic || t.Dynamic
			args = append(args, t.Text)
		}
		return args, true, dynamic
	}
	return nil, false, false
}

// MentionsCronwatch reports whether any command word is the cronwatch binary
// (for example "cronwatch serve" or "cronwatch prune").
func MentionsCronwatch(tokens []Token) bool {
	for _, t := range tokens {
		if !t.Operator && IsCronwatch(t.Text) {
			return true
		}
	}
	return false
}
