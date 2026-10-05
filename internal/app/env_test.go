package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSplitCommandReversesJoin(t *testing.T) {
	argv := []string{"sh", "-c", `echo "a b" 'c' \ $HOME`, "", "naïve"}
	got, err := splitCommand(joinCommand(argv))
	if err != nil || !slices.Equal(got, argv) {
		t.Fatalf("splitCommand = %q, %v", got, err)
	}
	if _, err := splitCommand("not quoted"); err == nil {
		t.Fatal("unquoted command accepted")
	}
}

// TestEnvdiffAndTry records a run under a minimal, cron-like environment and
// checks that envdiff explains it and try reproduces it.
func TestEnvdiffAndTry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mytool"), []byte("#!/bin/sh\necho \"tool in $PWD\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if err := Run(ctx, []string{"run", "--name", "Tool", "--data-dir", dir, "--", "mytool"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("ONLY_IN_CRON", "1")
	var exit *ExitError
	if err := Run(ctx, []string{"run", "--name", "Tool", "--data-dir", dir, "--", "mytool"}, &bytes.Buffer{}, &bytes.Buffer{}); !errors.As(err, &exit) || exit.Code != 127 {
		t.Fatalf("run without the tool on PATH: %v", err)
	}

	// Back in the "interactive shell".
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	os.Unsetenv("ONLY_IN_CRON")
	var out bytes.Buffer
	if err := Run(ctx, []string{"envdiff", "--data-dir", dir, "tool"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"failed) of Tool with this shell", "PATH\n", "Missing from run: " + bin, "Set only in run: ONLY_IN_CRON"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("envdiff output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := Run(ctx, []string{"envdiff", "--last-success", "--data-dir", dir, "tool"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "with the last success") || !strings.Contains(out.String(), "Missing from run: "+bin) {
		t.Fatalf("envdiff --last-success output:\n%s", out.String())
	}

	// try uses the failed run's PATH, so the tool is not found.
	var errOut bytes.Buffer
	if err := Run(ctx, []string{"try", "--data-dir", dir, "tool"}, &out, &errOut); !errors.As(err, &exit) || exit.Code != 127 {
		t.Fatalf("try: %v", err)
	}
	if !strings.Contains(errOut.String(), "mytool: command not found") {
		t.Fatalf("try stderr:\n%s", errOut.String())
	}

	// try from another directory still runs in the recorded one.
	t.Chdir(t.TempDir())
	runs := &bytes.Buffer{}
	if err := Run(ctx, []string{"runs", "--data-dir", dir, "tool"}, runs, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(runs.String()), "\n")
	successID := strings.Fields(lines[len(lines)-1])[0]
	out.Reset()
	if err := Run(ctx, []string{"try", "--data-dir", dir, "--run", successID}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if want := "tool in " + work; strings.TrimSpace(out.String()) != want {
		t.Fatalf("try output = %q, want %q", out.String(), want)
	}
}

func TestTryWithoutRecordedRunUsesCronDefaults(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	tab := filepath.Join(t.TempDir(), "crontab")
	if err := os.WriteFile(tab, []byte("* * * * * cronwatch run --name Env -- sh -c 'echo PATH=$PATH'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"sync", "--data-dir", dir, "--crontab", tab}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run(ctx, []string{"try", "--data-dir", dir, "env"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "PATH=/usr/bin:/bin" || !strings.Contains(errOut.String(), "cron's default environment") {
		t.Fatalf("out = %q, stderr = %q", out.String(), errOut.String())
	}
	out.Reset()
	if err := Run(ctx, []string{"try", "--data-dir", dir, "--env", "PATH=/bin", "env"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "PATH=/bin" {
		t.Fatalf("--env PATH: out = %q", out.String())
	}
	if err := Run(ctx, []string{"try", "--data-dir", dir, "--env", "novalue", "env"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("--env without = accepted")
	}
	if err := Run(ctx, []string{"envdiff", "--data-dir", dir, "env"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no run with a recorded environment") {
		t.Fatalf("envdiff without runs: %v", err)
	}
}

func TestJobOrRunArgs(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		runID string
		ok    bool
	}{{nil, "", false}, {[]string{"a"}, "", true}, {nil, "r", true}, {[]string{"a"}, "r", false}, {[]string{"a", "b"}, "", false}} {
		if _, err := jobOrRunArgs("envdiff", tc.args, tc.runID); (err == nil) != tc.ok {
			t.Errorf("jobOrRunArgs(%q, %q) = %v", tc.args, tc.runID, err)
		}
	}
}
