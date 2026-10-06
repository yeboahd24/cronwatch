package app

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/yeboahd24/cronwatch/internal/logs"
	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/runenv"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// lifecycle records runs from start to end and raises the alerts their ends
// call for. Every way a run ends goes through finish: the command finishing,
// an end ping, a start ping closing a run that never got one, a skip by
// --no-overlap, a heartbeat run timing out, and a run whose cronwatch
// process died. So each is recorded and alerted on the same way.
type lifecycle struct {
	s      *storage.Store
	stderr io.Writer // for warnings and hook failures
}

// start records a run of job starting at started, with the environment it
// starts in. A heartbeat run, from ping --start, has no owning process; any
// other belongs to this one. Failing to record the environment is only a
// warning, since the run itself is recorded.
func (l lifecycle) start(ctx context.Context, job model.Job, started time.Time, heartbeat bool) (model.Run, error) {
	create := l.s.CreateRun
	if heartbeat {
		create = l.s.CreateHeartbeatRun
	}
	run, err := create(ctx, job.ID, started)
	if err != nil {
		return run, err
	}
	if err := l.s.SetRunEnv(ctx, run.ID, runenv.Capture()); err != nil {
		fmt.Fprintf(l.stderr, "cronwatch: warning: could not record the environment: %v\n", err)
	}
	return run, nil
}

// finish records how a run of job ended, then queues and delivers any alert
// that changes the job between ok and failing. It fails only if the result
// cannot be recorded; trouble raising the alert is a warning.
func (l lifecycle) finish(ctx context.Context, job model.Job, runID string, c storage.Completion) (model.Run, error) {
	if err := l.s.CompleteRun(ctx, runID, c); err != nil {
		return model.Run{}, err
	}
	run, err := l.s.GetRun(ctx, runID)
	if err != nil {
		fmt.Fprintf(l.stderr, "cronwatch: warning: could not read the finished run for its alerts: %v\n", err)
		return run, nil
	}
	notifyRun(ctx, l.s, job, run, l.stderr)
	return run, nil
}

// expireHeartbeats times out the heartbeat runs of the job with jobID, or
// of every job if jobID is "", that have gone longer than the job's
// --max-duration without an end ping.
func (l lifecycle) expireHeartbeats(ctx context.Context, jobID string, now time.Time) {
	overdue, err := l.s.OverdueHeartbeatRuns(ctx, jobID, now)
	if err != nil {
		fmt.Fprintf(l.stderr, "cronwatch: warning: could not time out heartbeat runs: %v\n", err)
	}
	for _, o := range overdue {
		within := shortDuration(o.Limit)
		note := fmt.Sprintf("cronwatch: no end ping within %s (--max-duration)\n", within)
		l.finishFound(ctx, o.Run, storage.Completion{Ended: now, Duration: now.Sub(o.Run.StartedAt), Status: "timeout",
			Stderr: note, Combined: string(logs.StderrMark) + note, Reason: fmt.Sprintf("no end ping within %s (--max-duration)", within)})
	}
}

// reapAbandoned records as failed the runs whose cronwatch process died on
// this host without recording a result, keeping the output they had saved.
func (l lifecycle) reapAbandoned(ctx context.Context) error {
	abandoned, err := l.s.AbandonedRuns(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, run := range abandoned {
		note := fmt.Sprintf("cronwatch: run abandoned; owner process %d exited without recording a result\n", run.PID)
		l.finishFound(ctx, run, storage.Completion{Ended: now, Duration: now.Sub(run.StartedAt), Status: "failed",
			Stdout: run.Stdout, Stderr: storage.AppendLine(run.Stderr, note), Combined: storage.AppendLine(run.CombinedLog, string(logs.StderrMark)+note),
			Truncated: run.Truncated, Reason: fmt.Sprintf("the cronwatch process (pid %d) exited without recording a result", run.PID)})
	}
	return nil
}

// finishFound finishes a run that maintenance found ended without saying
// so. Another process may finish it first, which is not an error.
func (l lifecycle) finishFound(ctx context.Context, run model.Run, c storage.Completion) {
	job, err := l.s.GetJob(ctx, run.JobID)
	if err != nil {
		fmt.Fprintf(l.stderr, "cronwatch: warning: could not read the job of run %s: %v\n", run.ID, err)
		return
	}
	_, _ = l.finish(ctx, job, run.ID, c)
}
