package runenv

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestFromEnvironKeepsOnlyRecordedValues(t *testing.T) {
	e := FromEnviron([]string{"PATH=/usr/bin:/bin", "API_TOKEN=secret", "LC_TIME=C", "HOME=/home/a", "PATH=/ignored"})
	if got := e.Vars; len(got) != 3 || got["PATH"] != "/usr/bin:/bin" || got["LC_TIME"] != "C" || got["HOME"] != "/home/a" {
		t.Fatalf("vars = %v", got)
	}
	if !slices.Equal(e.Names, []string{"API_TOKEN", "HOME", "LC_TIME", "PATH"}) {
		t.Fatalf("names = %v", e.Names)
	}
	if !slices.Equal(e.Hidden(), []string{"API_TOKEN"}) {
		t.Fatalf("hidden = %v", e.Hidden())
	}
	if e.Dir == "" {
		t.Fatal("working directory not captured")
	}
}

func TestHashIsStable(t *testing.T) {
	a := FromEnviron([]string{"PATH=/bin", "B=1", "A=2"})
	b := FromEnviron([]string{"A=3", "B=4", "PATH=/bin"})
	if a.Hash() != b.Hash() {
		t.Fatal("hidden values or variable order changed the hash")
	}
	c := FromEnviron([]string{"PATH=/usr/bin", "A=2", "B=1"})
	if a.Hash() == c.Hash() {
		t.Fatal("a different PATH kept the same hash")
	}
}

func TestCompare(t *testing.T) {
	cron := Env{Dir: "/home/a", User: "a", Vars: map[string]string{"PATH": "/usr/bin:/bin", "SHELL": "/bin/sh"},
		Names: []string{"MAILTO", "PATH", "SHELL"}}
	shell := Env{Dir: "/home/a/project", User: "a", Vars: map[string]string{"PATH": "/home/a/.local/bin:/usr/local/bin:/usr/bin:/bin", "SHELL": "/bin/sh"},
		Names: []string{"NVM_DIR", "PATH", "SHELL"}}
	d := Compare(cron, shell)
	if len(d.Changes) != 2 || d.Changes[0].Field != "Working directory" || d.Changes[1].Field != "PATH" {
		t.Fatalf("changes = %+v", d.Changes)
	}
	path := d.Changes[1]
	if len(path.OnlyA) != 0 || !slices.Equal(path.OnlyB, []string{"/home/a/.local/bin", "/usr/local/bin"}) {
		t.Fatalf("PATH change = %+v", path)
	}
	if !slices.Equal(d.OnlyA, []string{"MAILTO"}) || !slices.Equal(d.OnlyB, []string{"NVM_DIR"}) {
		t.Fatalf("only = %v / %v", d.OnlyA, d.OnlyB)
	}
	if !Compare(cron, cron).Empty() {
		t.Fatal("an environment differs from itself")
	}
}

func TestLookPathUsesTheEnvironmentsPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	with := Env{Dir: "/", Vars: map[string]string{"PATH": "/nonexistent:" + dir}}
	if got, ok := with.LookPath("tool"); !ok || got != tool {
		t.Fatalf("LookPath = %q, %v", got, ok)
	}
	without := Env{Dir: "/", Vars: map[string]string{"PATH": "/nonexistent"}}
	if _, ok := without.LookPath("tool"); ok {
		t.Fatal("found a command outside PATH")
	}
	relative := Env{Dir: dir, Vars: map[string]string{}}
	if got, ok := relative.LookPath("./tool"); !ok || got != tool {
		t.Fatalf("relative LookPath = %q, %v", got, ok)
	}
}
