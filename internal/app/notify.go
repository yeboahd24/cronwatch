package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
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

// Alert retries back off from alertRetryMin, doubling up to alertRetryMax,
// until alertMaxAttempts attempts have failed: about seven hours in all.
const (
	alertMaxAttempts = 12
	alertRetryMin    = time.Minute
	alertRetryMax    = time.Hour
)

// hookOutputLimit bounds how much of a hook's output is kept, from its end.
const hookOutputLimit = 4 << 10

// alertRetryDelay is how long to wait after the given number of failed attempts.
func alertRetryDelay(attempts int) time.Duration {
	d := alertRetryMin
	for i := 1; i < attempts && d < alertRetryMax; i++ {
		d *= 2
	}
	return min(d, alertRetryMax)
}

// shortDuration formats d as a flag would take it: "1h" rather than "1h0m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// hookName is the flag that sets a hook, for messages.
func hookName(hook string) string {
	if hook == "on_recover" {
		return "--on-recover"
	}
	return "--on-failure"
}

// queueAlert records that the job's hook should run for e. It is delivered
// by the next deliverAlerts for the job.
func queueAlert(ctx context.Context, s *storage.Store, hook string, e hookEvent, stderr io.Writer) {
	a := storage.NewAlert{JobID: e.Job.ID, Event: e.Kind, Hook: hook, Environ: e.environ(), Created: time.Now()}
	if e.Run != nil {
		a.RunID = e.Run.ID
	}
	if err := s.QueueAlert(ctx, a); err != nil {
		fmt.Fprintf(stderr, "cronwatch: %s hook for %s not run: could not record the alert: %v\n", hookName(hook), e.Job.Name, err)
	}
}

// deliverAlerts attempts the due pending alerts of the job with jobID, or of
// every job if jobID is "". A job's alerts are delivered in the order they
// were raised, so one that is waiting to be retried holds back later ones.
func deliverAlerts(ctx context.Context, s *storage.Store, jobID string, stderr io.Writer) {
	pending, err := s.PendingAlerts(ctx, jobID)
	if err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not read pending alerts: %v\n", err)
		return
	}
	blocked := map[string]bool{}
	for _, a := range pending {
		if blocked[a.JobID] || ctx.Err() != nil {
			continue
		}
		if a.Command == "" {
			if err := s.CancelAlert(ctx, a.ID, hookName(a.Hook)+" hook was removed"); err != nil {
				fmt.Fprintf(stderr, "cronwatch: warning: could not cancel an alert: %v\n", err)
			}
			continue
		}
		if !deliverAlert(ctx, s, a, stderr) {
			blocked[a.JobID] = true
		}
	}
}

// deliverAlert makes one attempt at a due alert and records the result. It
// reports whether the alert is done with, delivered or given up on.
func deliverAlert(ctx context.Context, s *storage.Store, a storage.PendingAlert, stderr io.Writer) bool {
	now := time.Now()
	// The lease outlasts the hook's timeout, so it only runs out if this
	// process dies mid-attempt.
	claimed, err := s.ClaimAlert(ctx, a.ID, now, now.Add(hookTimeout+time.Minute))
	if err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not claim an alert: %v\n", err)
		return false
	}
	if !claimed {
		return false // not yet due, or another process has it
	}
	attempt := a.Attempts + 1
	env := append(slices.Clone(a.Environ), "CRONWATCH_ALERT_ID="+a.ID, "CRONWATCH_ATTEMPT="+strconv.Itoa(attempt))
	output, runErr := runHook(a.Command, env)
	status, errText := storage.AlertDelivered, ""
	var retryAt *time.Time
	if runErr != nil {
		errText = runErr.Error()
		status = storage.AlertUndelivered
		next := "giving up"
		if attempt < alertMaxAttempts {
			status = storage.AlertPending
			t := time.Now().Add(alertRetryDelay(attempt))
			retryAt = &t
			next = "retrying in " + shortDuration(alertRetryDelay(attempt))
		}
		fmt.Fprintf(stderr, "cronwatch: %s hook for %s failed: %v (attempt %d of %d, %s)\n",
			hookName(a.Hook), a.JobName, runErr, attempt, alertMaxAttempts, next)
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
	}
	// Record the result even if ctx was cancelled during the hook.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.FinishAlertAttempt(finishCtx, a.ID, status, retryAt, errText, output); err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not record the alert's delivery: %v\n", err)
	}
	return status != storage.AlertPending
}

// runHook runs command with sh and the extra environment, returning the end
// of its combined output.
func runHook(command string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	out := &tailBuffer{limit: hookOutputLimit}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), env...)
	// One writer for both streams makes exec share a single pipe, so Write
	// is never called concurrently.
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s", hookTimeout)
	}
	return out.String(), err
}

// tailBuffer keeps the last limit bytes written to it, holding at most twice
// that in memory.
type tailBuffer struct {
	limit   int
	buf     []byte
	dropped bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.limit {
		p = p[len(p)-b.limit:]
		b.dropped = true
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > 2*b.limit {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.limit:]...)
		b.dropped = true
	}
	return n, nil
}

// String returns the kept output, trimmed, starting with "…" if some was dropped.
func (b *tailBuffer) String() string {
	out := b.buf
	if len(out) > b.limit {
		out, b.dropped = out[len(out)-b.limit:], true
	}
	text := strings.TrimSpace(strings.ToValidUTF8(string(out), ""))
	if b.dropped && text != "" {
		text = "…" + text
	}
	return text
}

// notifyRun queues the job's hook if run changed the job between ok and
// failing, then delivers the job's due alerts, including retries of earlier
// ones. Runs that were cancelled or skipped, and runs of paused or archived
// jobs, raise no alert.
func notifyRun(ctx context.Context, s *storage.Store, job model.Job, run model.Run, stderr io.Writer) {
	defer deliverAlerts(ctx, s, job.ID, stderr)
	failing := model.Failing(run.Status)
	if (!failing && run.Status != "success") || (job.OnFailure == "" && job.OnRecover == "") || !job.Monitored(time.Now()) {
		return
	}
	wasFailing, err := s.WasFailingBefore(ctx, job.ID, run.StartedAt)
	if err != nil {
		fmt.Fprintf(stderr, "cronwatch: warning: could not check the previous status for hooks: %v\n", err)
		return
	}
	switch {
	case failing && !wasFailing && job.OnFailure != "":
		queueAlert(ctx, s, "on_failure", hookEvent{Kind: run.Status, Job: job, Run: &run}, stderr)
	case !failing && wasFailing && job.OnRecover != "":
		queueAlert(ctx, s, "on_recover", hookEvent{Kind: "recovered", Job: job, Run: &run}, stderr)
	}
}

// notifyMissed queues --on-failure alerts for jobs whose newly recorded
// missed occurrences turned them from ok to failing. maintain delivers them.
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
			queueAlert(ctx, s, "on_failure", hookEvent{Kind: "missed", Job: m.Job, ExpectedAt: first}, stderr)
		}
	}
}
