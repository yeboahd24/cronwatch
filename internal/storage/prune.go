package storage

import (
	"context"
	"errors"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// PruneResult counts the rows Prune deleted.
type PruneResult struct {
	Runs, MissedOccurrences int64
}

// Prune deletes finished runs beyond the newest keep per job (keep 0 means no
// limit) and runs and missed occurrences older than before (zero means no age
// limit). Running runs are never deleted.
func (s *Store) Prune(ctx context.Context, keep int, before time.Time) (PruneResult, error) {
	var result PruneResult
	if keep < 0 {
		return result, errors.New("keep must be non-negative")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	q := db.New(tx)
	if keep > 0 {
		n, err := q.DeleteRunsBeyondKeep(ctx, int64(keep-1))
		if err != nil {
			return result, err
		}
		result.Runs += n
	}
	if !before.IsZero() {
		n, err := q.DeleteRunsBefore(ctx, timestamp(before))
		if err != nil {
			return result, err
		}
		result.Runs += n
		if result.MissedOccurrences, err = q.DeleteMissedBefore(ctx, timestamp(before)); err != nil {
			return result, err
		}
	}
	if _, err := q.DeleteUnusedEnvironments(ctx); err != nil {
		return result, err
	}
	return result, tx.Commit()
}
