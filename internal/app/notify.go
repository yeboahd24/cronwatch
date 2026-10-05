package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// Environment variables that set a job's hooks when --on-failure or
// --on-recover is not passed. Set at the top of a crontab, they apply to
// every job in it.
const (
	envOnFailure = "CRONWATCH_ON_FAILURE"
	envOnRecover = "CRONWATCH_ON_RECOVER"
)

// hookTimeout bounds a hook, so a hung notifier cannot hold up the job's exit
// or the dashboard's maintenance loop for long.
const hookTimeout = 30 * time.Second

// hookEvent is a job changing between ok and failing.
type hookEvent struct {
	Kind       string // "failed", "timeout", "missed" or "recovered"
	Job        model.Job
	Run        *model.Run // nil for missed
	ExpectedAt time.Time  // for missed
}

func (e hookEvent) environ() []string {
	env := []string{
		"CRONWATCH_EVENT=" + e.Kind,
		"CRONWATCH_JOB_NAME=" + e.Job.Name,
		"CRONWATCH_JOB_SLUG=" + e.Job.Slug,
	}
	if host, err := os.Hostname(); err == nil {
		env = append(env, "CRONWATCH_HOST="+host)
	}
	if r := e.Run; r != nil {
		env = append(env, "CRONWATCH_STATUS="+r.Status, "CRONWATCH_RUN_ID="+r.ID,
			"CRONWATCH_STARTED_AT="+r.StartedAt.UTC().Format(time.RFC3339), "CRONWATCH_REASON="+r.Reason,
			"CRONWATCH_LAST_ERROR="+logs.LastError(logs.ParseAs(r.Stderr, logs.Stderr)))
		if r.ExitCode != nil {
			env = append(env, "CRONWATCH_EXIT_CODE="+strconv.Itoa(*r.ExitCode))
		}
	} else {
		env = append(env, "CRONWATCH_STATUS=missed", "CRONWATCH_EXPECTED_AT="+e.ExpectedAt.UTC().Format(time.RFC3339))
	}
	return env
}

// runHook runs command with sh, passing the event in CRONWATCH_* variables.
// Its output is shown only if it fails; a failing hook never changes the
// job's result.
func runHook(command, flag string, e hookEvent, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), e.environ()...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if err == nil {
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s", hookTimeout)
	}
	output := strings.TrimSpace(out.String())
	if len(output) > 2000 {
		output = "…" + output[len(output)-2000:]
	}
	fmt.Fprintf(stderr, "cronwatch: %s hook for %s failed: %v\n", flag, e.Job.Name, err)
	if output != "" {
		fmt.Fprintln(stderr, output)
	}
}

// notifyRun runs the job's hook if run changed the job between ok and
// failing. Runs that were cancelled or skipped change nothing.
func notifyRun(ctx context.Context, s *storage.Store, job model.Job, run model.Run, stderr io.Writer) {
	failing := model.Failing(run.Status)
	if (!failing && run.Status != "success") || (job.OnFailure == "" && job.OnRecover == "") {
		return
	}
	wasFailing, err := s.WasFailingBefore(ctx, job.ID, run.StartedAt)
	if err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not check the previous status for hooks: %v\n", err)
		return
	}
	switch {
	case failing && !wasFailing && job.OnFailure != "":
		runHook(job.OnFailure, "--on-failure", hookEvent{Kind: run.Status, Job: job, Run: &run}, stderr)
	case !failing && wasFailing && job.OnRecover != "":
		runHook(job.OnRecover, "--on-recover", hookEvent{Kind: "recovered", Job: job, Run: &run}, stderr)
	}
}

// notifyMissed runs --on-failure hooks for jobs whose newly recorded missed
// occurrences turned them from ok to failing.
func notifyMissed(ctx context.Context, s *storage.Store, missed []storage.NewlyMissed, stderr io.Writer) {
	for _, m := range missed {
		if m.Job.OnFailure == "" {
			continue
		}
		first := m.ExpectedAt[0]
		wasFailing, err := s.WasFailingBefore(ctx, m.Job.ID, first)
		if err != nil {
			fmt.Fprintf(stderr, "cronwatch: warning: could not check %s's previous status for hooks: %v\n", m.Job.Name, err)
			continue
		}
		if !wasFailing {
			runHook(m.Job.OnFailure, "--on-failure", hookEvent{Kind: "missed", Job: m.Job, ExpectedAt: first}, stderr)
		}
	}
}
