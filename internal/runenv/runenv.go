// Package runenv captures the environment a job runs in and compares two
// environments, to explain why a command behaves differently under cron.
package runenv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Env is a run's environment. Values are kept only for variables that shape
// how a command is found and run; other variables may hold secrets, so only
// their names are kept.
type Env struct {
	Dir   string            `json:"dir"`
	User  string            `json:"user"`
	UID   string            `json:"uid"`
	Vars  map[string]string `json:"vars"`  // values of Recorded variables
	Names []string          `json:"names"` // names of all variables, sorted
}

// recorded lists the variables whose values are kept.
var recorded = []string{"PATH", "HOME", "SHELL", "USER", "LOGNAME", "LANG", "LANGUAGE", "TZ", "TMPDIR"}

// Recorded reports whether the value of variable name is kept.
func Recorded(name string) bool {
	return slices.Contains(recorded, name) || strings.HasPrefix(name, "LC_")
}

// sessionPrefixes and sessionNames match variables that terminals and desktop
// sessions set. They are always missing under cron and almost never the cause
// of a failure, so comparisons can leave them out.
var (
	sessionPrefixes = []string{"XDG_", "GNOME_", "GDM", "GTK_", "GDK_", "GJS_", "QT_", "DBUS_", "WAYLAND_",
		"TERM", "MEMORY_PRESSURE_", "KDE_", "VTE_", "WT_", "ITERM_", "KONSOLE_", "TMUX"}
	sessionNames = []string{"_", "COLORTERM", "DESKTOP_SESSION", "DISPLAY", "INVOCATION_ID", "JOURNAL_STREAM",
		"LESS", "LSCOLORS", "LS_COLORS", "MANAGERPID", "OLDPWD", "PAGER", "PWD", "SESSION_MANAGER", "SHLVL",
		"STY", "SYSTEMD_EXEC_PID", "WINDOWID", "XAUTHORITY", "XMODIFIERS"}
)

// Session reports whether name is a terminal or desktop session variable.
func Session(name string) bool {
	if slices.Contains(sessionNames, name) {
		return true
	}
	for _, p := range sessionPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// WithoutSession returns d without session variables in OnlyA and OnlyB, and
// how many it left out.
func (d Diff) WithoutSession() (Diff, int) {
	n := 0
	keep := func(names []string) []string {
		var out []string
		for _, name := range names {
			if Session(name) {
				n++
			} else {
				out = append(out, name)
			}
		}
		return out
	}
	d.OnlyA, d.OnlyB = keep(d.OnlyA), keep(d.OnlyB)
	return d, n
}

// Capture returns the current process's environment.
func Capture() Env {
	return FromEnviron(os.Environ())
}

// FromEnviron builds an Env from KEY=VALUE pairs, the working directory and
// the current user.
func FromEnviron(environ []string) Env {
	e := Env{Vars: map[string]string{}}
	e.Dir, _ = os.Getwd()
	if u, err := user.Current(); err == nil {
		e.User, e.UID = u.Username, u.Uid
	} else {
		e.UID = strconv.Itoa(os.Getuid())
	}
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || slices.Contains(e.Names, name) {
			continue
		}
		e.Names = append(e.Names, name)
		if Recorded(name) {
			e.Vars[name] = value
		}
	}
	slices.Sort(e.Names)
	return e
}

// Hash identifies an environment, so identical ones are stored once.
func (e Env) Hash() string {
	data, _ := json.Marshal(e) // map keys are sorted, so this is canonical
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Hidden returns the names of variables whose values are not kept.
func (e Env) Hidden() []string {
	var out []string
	for _, name := range e.Names {
		if _, ok := e.Vars[name]; !ok {
			out = append(out, name)
		}
	}
	return out
}

// Environ returns the recorded variables as KEY=VALUE pairs, sorted.
func (e Env) Environ() []string {
	out := make([]string, 0, len(e.Vars))
	for name, value := range e.Vars {
		out = append(out, name+"="+value)
	}
	slices.Sort(out)
	return out
}

// CronDefault approximates the environment cron gives a job: the user's
// login variables, /bin/sh, a minimal PATH, and the home directory.
func CronDefault() Env {
	e := Env{Vars: map[string]string{"SHELL": "/bin/sh", "PATH": "/usr/bin:/bin"}}
	if u, err := user.Current(); err == nil {
		e.User, e.UID, e.Dir = u.Username, u.Uid, u.HomeDir
		e.Vars["HOME"], e.Vars["LOGNAME"], e.Vars["USER"] = u.HomeDir, u.Username, u.Username
	}
	for name := range e.Vars {
		e.Names = append(e.Names, name)
	}
	slices.Sort(e.Names)
	return e
}

// LookPath finds an executable the way a shell with this environment's PATH
// would, so a command missing from cron's PATH is reported as missing.
func (e Env) LookPath(file string) (string, bool) {
	if strings.Contains(file, "/") {
		if !filepath.IsAbs(file) {
			file = filepath.Join(e.Dir, file)
		}
		return file, executable(file)
	}
	for _, dir := range filepath.SplitList(e.Vars["PATH"]) {
		if dir == "" {
			dir = "."
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(e.Dir, dir)
		}
		if path := filepath.Join(dir, file); executable(path) {
			return path, true
		}
	}
	return "", false
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// Change is one difference between two environments. For PATH, OnlyA and
// OnlyB list the directories present in only one of them.
type Change struct {
	Field        string // "Working directory", "User", or a variable name
	A, B         string // "" when unset
	OnlyA, OnlyB []string
}

// Diff is the difference between environment A and environment B.
type Diff struct {
	Changes      []Change
	OnlyA, OnlyB []string // variables set in only one, by name
}

// Empty reports whether the environments match as far as they were recorded.
func (d Diff) Empty() bool {
	return len(d.Changes) == 0 && len(d.OnlyA) == 0 && len(d.OnlyB) == 0
}

// Compare returns the differences between a and b. Variables whose values are
// not recorded are compared by name only.
func Compare(a, b Env) Diff {
	var d Diff
	if a.Dir != b.Dir {
		d.Changes = append(d.Changes, Change{Field: "Working directory", A: a.Dir, B: b.Dir})
	}
	if a.User != b.User {
		d.Changes = append(d.Changes, Change{Field: "User", A: a.User, B: b.User})
	}
	inA, inB := map[string]bool{}, map[string]bool{}
	for _, n := range a.Names {
		inA[n] = true
	}
	for _, n := range b.Names {
		inB[n] = true
	}
	for _, name := range a.Names {
		if !inB[name] {
			d.OnlyA = append(d.OnlyA, name)
			continue
		}
		va, okA := a.Vars[name]
		vb, okB := b.Vars[name]
		if !okA || !okB || va == vb {
			continue
		}
		c := Change{Field: name, A: va, B: vb}
		if name == "PATH" {
			da, db := filepath.SplitList(va), filepath.SplitList(vb)
			c.OnlyA = missingFrom(da, db)
			c.OnlyB = missingFrom(db, da)
		}
		d.Changes = append(d.Changes, c)
	}
	for _, name := range b.Names {
		if !inA[name] {
			d.OnlyB = append(d.OnlyB, name)
		}
	}
	return d
}

// missingFrom returns the items of list that other does not contain.
func missingFrom(list, other []string) []string {
	var out []string
	for _, item := range list {
		if !slices.Contains(other, item) && !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}
