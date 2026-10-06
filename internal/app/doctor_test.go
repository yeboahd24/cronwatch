package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/storage"
)

// doctor runs cronwatch doctor and returns its output and exit code.
func doctor(t *testing.T, dir string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), []string{"doctor", "--data-dir", dir}, &out, &bytes.Buffer{})
	code := 0
	if exit, ok := errors.AsType[*ExitError](err); ok {
		code = exit.Code
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

// fakeProcesses makes doctor see the given processes.
func fakeProcesses(t *testing.T, procs ...process) {
	t.Helper()
	saved := listProcesses
	listProcesses = func() ([]process, bool) { return procs, true }
	t.Cleanup(func() { listProcesses = saved })
}

func TestDoctorBeforeFirstRun(t *testing.T) {
	setCrontab := fakeCrontab(t)
	setCrontab("")
	fakeProcesses(t, process{PID: 1, Argv: []string{"/usr/sbin/cron"}})
	dir := filepath.Join(t.TempDir(), "data")
	out, code := doctor(t, dir)
	if code != 0 || !strings.Contains(out, "does not exist yet") || !strings.Contains(out, "No problems found.") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("doctor created the data directory")
	}
}

func TestDoctor(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	bin := t.TempDir()
	cw := filepath.Join(bin, "cronwatch")
	if err := os.WriteFile(cw, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setCrontab := fakeCrontab(t)
	setCrontab("PATH=" + bin + ":/usr/bin:/bin\nCRONWATCH_DATA_DIR=" + dir + "\n" +
		"0 1 * * * cronwatch run --name Good -- true\n" +
		"0 2 * * * cronwatch run --data-dir /elsewhere --name Elsewhere -- true\n" +
		"0 3 * * * /nope/cronwatch run --name Lost -- true\n" +
		"0 4 * * * docker system prune -f\n")
	s := openStore(t, dir)
	expr := "0 1 * * *"
	hook := "exit 1"
	job, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "good", Name: "Good", Command: `"true"`, Schedule: &expr, OnFailure: &hook})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := s.UpsertJob(ctx, storage.JobSpec{Slug: "nap", Name: "Nap", Command: `"true"`})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PauseJob(ctx, paused, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	// No cron daemon, and serve running for another data directory.
	fakeProcesses(t, process{PID: 7, Argv: []string{"cronwatch", "serve", "--data-dir", "/other"}})

	out, code := doctor(t, dir)
	for _, want := range []string{
		"Line 4 records to /elsewhere, not " + dir,
		"Line 5: cron cannot find /nope/cronwatch",
		"4 scheduled lines, 3 run through cronwatch run",
		"! 1 line is not monitored: line 6",
		"cronwatch sync --wrap",
		"! No cron daemon is running",
		"✗ Missed runs have never been checked",
		"· 1 job paused: Nap",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
	if code != 1 || strings.Contains(out, "cronwatch serve is running") || strings.Contains(out, "Line 3") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	// Once missed runs are checked and serve runs for this directory, those
	// findings clear; an old check and an undelivered alert are warnings.
	fakeProcesses(t, process{PID: 1, Argv: []string{"cron"}},
		process{PID: 42, Argv: []string{"/usr/local/bin/cronwatch", "serve"}, Env: []string{"CRONWATCH_DATA_DIR=" + dir}})
	if err := maintain(ctx, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	out, _ = doctor(t, dir)
	if !strings.Contains(out, "✓ cronwatch serve is running for this data directory (pid 42)") || !strings.Contains(out, "✓ Missed runs were last checked") {
		t.Fatalf("after a check:\n%s", out)
	}
	if err := s.SetMaintained(ctx, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	queueAlert(ctx, s, "on_failure", hookEvent{Kind: "missed", Job: job, ExpectedAt: time.Now()}, &bytes.Buffer{})
	deliverAlerts(ctx, s, "", &bytes.Buffer{})
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _ = doctor(t, dir)
	for _, want := range []string{
		"! Missed runs were last checked 1h ago",
		"! 1 alert could not be delivered: Good",
		"has mode 0755; other users can read it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
