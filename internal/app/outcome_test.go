package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runner"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestJudge(t *testing.T) {
	ok := runner.Result{Status: "success", Stdout: "Backup complete\n"}
	for _, tc := range []struct {
		name         string
		rules        runRules
		res          runner.Result
		status, want string // want is a substring of the reason; "" means none
	}{
		{"plain success", runRules{}, ok, "success", ""},
		{"plain failure", runRules{}, runner.Result{Status: "failed", ExitCode: 2}, "failed", ""},
		{"ok code", runRules{OKCodes: []int{3}}, runner.Result{Status: "failed", ExitCode: 3}, "success", "exit code 3 is listed"},
		{"code not listed", runRules{OKCodes: []int{3}}, runner.Result{Status: "failed", ExitCode: 4}, "failed", ""},
		{"stderr", runRules{FailOnStderr: true}, runner.Result{Status: "success", Stderr: "\nwarning: disk low\n"}, "failed", "wrote to stderr (--fail-on-stderr): warning: disk low"},
		{"blank stderr", runRules{FailOnStderr: true}, runner.Result{Status: "success", Stderr: " \n"}, "success", ""},
		{"fail match", runRules{FailMatch: regexp.MustCompile(`ERROR|Traceback`)}, runner.Result{Status: "success", Stdout: "ok\nERROR: lost\n"}, "failed", `matched --fail-if-match "ERROR|Traceback": ERROR: lost`},
		{"success match missing", runRules{SuccessMatch: regexp.MustCompile(`complete`)}, runner.Result{Status: "success", Stdout: "partial\n"}, "failed", "did not match --success-if-match"},
		{"success match found", runRules{SuccessMatch: regexp.MustCompile(`complete`)}, ok, "success", ""},
		{"rules apply after ok codes", runRules{OKCodes: []int{3}, FailMatch: regexp.MustCompile(`ERROR`)}, runner.Result{Status: "failed", ExitCode: 3, Stderr: "ERROR"}, "failed", "--fail-if-match"},
		{"timeout", runRules{Timeout: time.Minute}, runner.Result{Status: "timeout", ExitCode: 143}, "timeout", "timed out after 1m0s"},
		{"cancelled ignores rules", runRules{FailOnStderr: true}, runner.Result{Status: "cancelled", Stderr: "x"}, "cancelled", ""},
		{"patterns match one line", runRules{FailMatch: regexp.MustCompile(`^ERROR$`)}, runner.Result{Status: "success", Stdout: "ok\r\nERROR\r\n"}, "failed", "--fail-if-match"},
		{"patterns do not span lines", runRules{FailMatch: regexp.MustCompile(`ok\nERROR`)}, runner.Result{Status: "success", Stdout: "ok\nERROR\n"}, "success", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Feed the output through the rules as the runner would.
			var seen outputSeen
			watch := tc.rules.watch(&seen)
			for _, out := range []struct {
				text   string
				stderr bool
			}{{tc.res.Stdout, false}, {tc.res.Stderr, true}} {
				for line := range strings.Lines(out.text) {
					watch([]byte(strings.TrimSuffix(line, "\n")), out.stderr)
				}
			}
			status, reason := tc.rules.judge(tc.res, seen)
			if status != tc.status || (tc.want == "") != (reason == "") || !strings.Contains(reason, tc.want) {
				t.Fatalf("judge = %q, %q; want %q, reason containing %q", status, reason, tc.status, tc.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		status string
		code   int
		strict bool
		want   int
	}{
		{"success", 0, false, 0}, {"failed", 0, false, 0}, {"failed", 2, false, 2}, {"success", 3, false, 3},
		{"success", 3, true, 0}, {"failed", 0, true, 1}, {"failed", 2, true, 2}, {"timeout", 143, true, 143},
		{"skipped", 0, true, 0},
	} {
		if got := exitCode(tc.status, tc.code, tc.strict); got != tc.want {
			t.Errorf("exitCode(%q, %d, %v) = %d, want %d", tc.status, tc.code, tc.strict, got, tc.want)
		}
	}
}

func lastRun(t *testing.T, dir, slug string) model.Run {
	t.Helper()
	s, err := storage.Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	job, err := s.GetJobBySlug(context.Background(), slug)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListRunsForJob(context.Background(), job.ID, 1)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	return runs[0]
}

func TestRunAppliesRules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	run := func(args ...string) (error, string) {
		var errOut bytes.Buffer
		err := Run(ctx, append([]string{"run", "--data-dir", dir}, args...), &bytes.Buffer{}, &errOut)
		return err, errOut.String()
	}

	// A rule failure is recorded, but cron still sees the command's exit 0.
	err, errOut := run("--name", "match", "--fail-if-match", "ERROR", "--", "sh", "-c", "echo 'ERROR: disk full'")
	if err != nil {
		t.Fatalf("default exit: %v", err)
	}
	if r := lastRun(t, dir, "match"); r.Status != "failed" || !strings.Contains(r.Reason, "ERROR: disk full") || !strings.Contains(errOut, "cronwatch: failed: output matched") {
		t.Fatalf("run = %+v, stderr = %q", r, errOut)
	}
	// --strict-exit makes the exit code follow the recorded status.
	var exit *ExitError
	if err, _ := run("--name", "match", "--strict-exit", "--fail-if-match", "ERROR", "--", "sh", "-c", "echo ERROR"); !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("strict exit: %v", err)
	}
	if err, _ := run("--name", "codes", "--ok-codes", "3", "--strict-exit", "--", "sh", "-c", "exit 3"); err != nil {
		t.Fatalf("ok code with --strict-exit: %v", err)
	}
	if r := lastRun(t, dir, "codes"); r.Status != "success" || *r.ExitCode != 3 {
		t.Fatalf("ok code run = %+v", r)
	}

	// Rules see the whole output, not only what fits in --max-log-bytes.
	middle := "seq 100; echo 'ERROR: lost'; seq 1000"
	if err, _ := run("--name", "middle", "--max-log-bytes", "64", "--fail-if-match", "ERROR", "--", "sh", "-c", middle); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, dir, "middle"); r.Status != "failed" || !r.Truncated || strings.Contains(r.Stdout, "ERROR") || !strings.Contains(r.Reason, "ERROR: lost") {
		t.Fatalf("truncated fail match = %+v", r)
	}
	if err, _ := run("--name", "marker", "--max-log-bytes", "64", "--success-if-match", "^Backup complete$", "--", "sh", "-c", "seq 100; echo 'Backup complete'; seq 1000"); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, dir, "marker"); r.Status != "success" || !r.Truncated {
		t.Fatalf("truncated success match = %+v", r)
	}
	if err, _ := run("--name", "quiet", "--max-log-bytes", "0", "--fail-on-stderr", "--", "sh", "-c", "echo oops >&2"); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, dir, "quiet"); r.Status != "failed" || !strings.Contains(r.Reason, "oops") {
		t.Fatalf("unkept stderr = %+v", r)
	}

	started := time.Now()
	if err, _ := run("--name", "slow", "--timeout", "200ms", "--", "sh", "-c", "sleep 30"); !errors.As(err, &exit) || exit.Code != 143 {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("--timeout did not stop the command")
	}
	if r := lastRun(t, dir, "slow"); r.Status != "timeout" || !strings.Contains(r.Reason, "200ms") || r.Usage == nil {
		t.Fatalf("timeout run = %+v", r)
	}

	for _, bad := range [][]string{{"--ok-codes", "x"}, {"--ok-codes", "300"}, {"--fail-if-match", "("}, {"--timeout", "-1s"}} {
		if err, _ := run(append(append([]string{"--name", "bad"}, bad...), "--", "true")...); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestOverlap(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	// The first run holds the job lock until the test creates the gate file.
	firstDone := make(chan error, 1)
	gate := filepath.Join(dir, "gate")
	go func() {
		firstDone <- Run(ctx, []string{"run", "--name", "job", "--data-dir", dir, "--", "sh", "-c", "touch " + gate + ".started; while [ ! -e " + gate + " ]; do sleep 0.05; done"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	waitFor(t, gate+".started")
	first := lastRun(t, dir, "job")

	var errOut bytes.Buffer
	if err := Run(ctx, []string{"run", "--name", "job", "--no-overlap", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &errOut); err != nil {
		t.Fatalf("skipped run: %v", err)
	}
	skipped := lastRun(t, dir, "job")
	if skipped.Status != "skipped" || skipped.OverlappedRunID != first.ID || !strings.Contains(errOut.String(), "skipped: run "+first.ID) {
		t.Fatalf("skipped run = %+v, stderr = %q", skipped, errOut.String())
	}
	if err := Run(ctx, []string{"run", "--name", "job", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, dir, "job"); r.Status != "success" || r.OverlappedRunID != first.ID {
		t.Fatalf("overlapping run = %+v", r)
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	// With the first run finished, the lock is free again.
	if err := Run(ctx, []string{"run", "--name", "job", "--no-overlap", "--data-dir", dir, "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if r := lastRun(t, dir, "job"); r.Status != "success" || r.OverlappedRunID != "" {
		t.Fatalf("run after release = %+v", r)
	}
}

// waitFor waits up to 5 seconds for path to exist.
func waitFor(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s was not created", path)
}

func TestStorageErrors(t *testing.T) {
	ctx := context.Background()
	// A directory where the database should be makes it unopenable, while
	// the data directory, and so the job lock, still work.
	brokenDB := t.TempDir()
	if err := os.Mkdir(filepath.Join(brokenDB, "cronwatch.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file as the data directory breaks the lock too.
	noDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(noDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	command := []string{"--", "sh", "-c", "echo ran >> " + marker + "; echo ERROR; exit 3"}
	run := func(dir string, extra ...string) (error, string) {
		_ = os.Remove(marker)
		var errOut bytes.Buffer
		args := append(append([]string{"run", "--name", "job", "--data-dir", dir}, extra...), command...)
		return Run(ctx, args, &bytes.Buffer{}, &errOut), errOut.String()
	}
	ran := func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}

	// By default nothing runs when the run cannot be recorded.
	if err, _ := run(brokenDB); err == nil || ran() {
		t.Fatalf("default: err = %v, ran = %v", err, ran())
	}
	// With run, the command runs unrecorded and exits with its own code.
	err, errOut := run(brokenDB, "--on-storage-error", "run")
	if exit, ok := errors.AsType[*ExitError](err); !ok || exit.Code != 3 || !ran() || !strings.Contains(errOut, "this run will not be recorded (--on-storage-error run)") {
		t.Fatalf("run: err = %v, ran = %v, stderr = %q", err, ran(), errOut)
	}
	// Rules still decide the status, and so the --strict-exit code.
	if err, _ := run(brokenDB, "--on-storage-error", "run", "--ok-codes", "3", "--fail-if-match", "ERROR", "--strict-exit"); !errors.As(err, new(*ExitError)) || err.(*ExitError).Code != 3 {
		t.Fatalf("rules: err = %v", err)
	}
	// The crontab variable sets it; the flag takes precedence.
	t.Setenv(envOnStorageError, "run")
	if err, _ := run(brokenDB); !ran() {
		t.Fatalf("from the environment: err = %v", err)
	}
	if err, _ := run(brokenDB, "--on-storage-error", "fail"); err == nil || ran() {
		t.Fatalf("flag over environment: err = %v, ran = %v", err, ran())
	}
	t.Setenv(envOnStorageError, "maybe")
	if err, _ := run(brokenDB); err == nil || ran() {
		t.Fatalf("invalid environment value: err = %v", err)
	}
	t.Setenv(envOnStorageError, "")
	if err, _ := run(brokenDB, "--on-storage-error", "maybe"); err == nil || ran() {
		t.Fatalf("invalid flag value: err = %v", err)
	}

	// Without the lock, --no-overlap cannot be kept, so nothing runs; without
	// --no-overlap the command runs.
	if err, _ := run(noDir, "--on-storage-error", "run", "--no-overlap"); err == nil || ran() || !strings.Contains(err.Error(), "--no-overlap cannot check") {
		t.Fatalf("no lock with --no-overlap: err = %v, ran = %v", err, ran())
	}
	if run(noDir, "--on-storage-error", "run"); !ran() {
		t.Fatal("no lock without --no-overlap did not run")
	}
	// With the lock held by another run, --no-overlap skips as usual.
	lock, locked, err := lockJob(brokenDB, "job")
	if err != nil || !locked {
		t.Fatalf("lock: %v, %v", locked, err)
	}
	defer lock.Close()
	if err, errOut := run(brokenDB, "--on-storage-error", "run", "--no-overlap"); err != nil || ran() || !strings.Contains(errOut, "skipped") {
		t.Fatalf("held lock: err = %v, ran = %v, stderr = %q", err, ran(), errOut)
	}
}
