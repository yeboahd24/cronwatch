package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yeboahd24/cronwatch/internal/model"
	"github.com/yeboahd24/cronwatch/internal/storage"
)

// lockJob takes the job's lock without waiting. locked is false when another
// "cronwatch run" of the job holds it. The lock is released when the returned
// file is closed or the process exits, so a killed run never leaves it held.
func lockJob(dataDir, slug string) (f *os.File, locked bool, err error) {
	dir := filepath.Join(dataDir, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(filepath.Join(dir, slug+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return f, false, nil
	}
	if err != nil {
		f.Close()
		return nil, false, err
	}
	return f, true, nil
}

// skipRun records run as skipped because the job's previous run is still
// running. It exits 0: skipping is what --no-overlap asked for.
func skipRun(ctx context.Context, runs lifecycle, job model.Job, run model.Run, previous string) error {
	reason := "another run of this job was still running (--no-overlap)"
	if previous != "" {
		reason = fmt.Sprintf("run %s was still running (--no-overlap)", previous)
	}
	fmt.Fprintf(runs.stderr, "cronwatch: skipped: %s\n", reason)
	_, err := runs.finish(ctx, job, run.ID, storage.Completion{Ended: time.Now().UTC(), Status: "skipped", Reason: reason})
	if err == nil && previous != "" {
		err = runs.s.SetRunOverlap(ctx, run.ID, previous)
	}
	return err
}
