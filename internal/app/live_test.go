package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runner"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

func TestOutputIsSavedWhileRunning(t *testing.T) {
	saved := liveEvery
	liveEvery = 20 * time.Millisecond
	t.Cleanup(func() { liveEvery = saved })
	ctx := context.Background()
	dir := t.TempDir()
	gate := filepath.Join(t.TempDir(), "gate")
	done := make(chan error, 1)
	script := "echo starting; echo warming up >&2; while [ ! -e " + gate + " ]; do sleep 0.02; done; echo finished"
	go func() {
		done <- Run(ctx, []string{"run", "--data-dir", dir, "--name", "Long", "--no-echo", "--", "sh", "-c", script}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	s := openStore(t, dir)
	// While it runs, the output so far is saved.
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := latestRun(ctx, s, "long")
		// The streams are separate pipes, so their lines may arrive in either order.
		if r != nil && r.Status == "running" && r.Stdout == "starting\n" && r.Stderr == "warming up\n" && len(r.CombinedLog) == len("starting\n\x02warming up\n") && r.OutputAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("output not saved while running: %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r := lastRun(t, dir, "long")
	if r.Status != "success" || r.Stdout != "starting\nfinished\n" {
		t.Fatalf("finished run = %+v", r)
	}
	// Output saved late does not overwrite a finished run.
	if err := s.SaveRunOutput(ctx, r.ID, runner.Output{Stdout: "stale"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if again := lastRun(t, dir, "long"); again.Stdout != r.Stdout {
		t.Fatalf("a late save overwrote the result: %q", again.Stdout)
	}
}

func TestAbandonedRunKeepsSavedOutput(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, t.TempDir())
	if err := Run(ctx, []string{"run", "--data-dir", s.DataDir, "--name", "Job", "--", "true"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	job, err := s.GetJobBySlug(ctx, "job")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, job.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRunOutput(ctx, run.ID, runner.Output{Stdout: "step 1 done\n", Combined: "step 1 done\n"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The run's owner is a process that has exited, as if it was killed.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "UPDATE runs SET pid = ? WHERE id = ?", dead.Process.Pid, run.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReapAbandonedRuns(ctx); err != nil || n != 1 {
		t.Fatalf("reaped %d, %v", n, err)
	}
	reaped, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reaped.Status != "failed" || !strings.HasPrefix(reaped.CombinedLog, "step 1 done\n") || !strings.Contains(reaped.Stderr, "abandoned") {
		t.Fatalf("reaped run = %+v", reaped)
	}
}

// latestRun returns the job's newest run, or nil before the job has one.
func latestRun(ctx context.Context, s *storage.Store, slug string) *model.Run {
	job, err := s.GetJobBySlug(ctx, slug)
	if err != nil {
		return nil
	}
	runs, err := s.ListRunsForJob(ctx, job.ID, 1)
	if err != nil || len(runs) == 0 {
		return nil
	}
	return &runs[0]
}
