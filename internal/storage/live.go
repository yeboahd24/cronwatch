package storage

import (
	"context"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/runner"
)

// SaveRunOutput saves the output so far of a run still running, so it can
// be looked at before the run ends. It does nothing to a finished run.
func (s *Store) SaveRunOutput(ctx context.Context, id string, out runner.Output, at time.Time) error {
	truncated := int64(0)
	if out.Truncated {
		truncated = 1
	}
	_, err := db.New(s.DB).SaveRunOutput(ctx, db.SaveRunOutputParams{ID: id, Stdout: out.Stdout, Stderr: out.Stderr,
		CombinedLog: out.Combined, Truncated: truncated, OutputAt: nullTimestamp(&at)})
	return err
}
