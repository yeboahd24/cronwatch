package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
	"github.com/yeboahd24/cronwatch/internal/model"
)

// PauseJob stops monitoring the job until until, or until it is resumed if
// until is nil. Pausing a paused job replaces its pause.
func (s *Store) PauseJob(ctx context.Context, j model.Job, now time.Time, until *time.Time) error {
	if j.ArchivedAt != nil {
		return fmt.Errorf("%s is archived; resume it first", j.Name)
	}
	return db.New(s.DB).PauseJob(ctx, db.PauseJobParams{ID: j.ID, PausedAt: nullTimestamp(&now), PausedUntil: nullTimestamp(until), UpdatedAt: timestamp(now)})
}

// ArchiveJob stops monitoring the job and leaves it off job lists, keeping
// its runs. It replaces any pause.
func (s *Store) ArchiveJob(ctx context.Context, j model.Job, now time.Time) error {
	return db.New(s.DB).ArchiveJob(ctx, db.ArchiveJobParams{ID: j.ID, ArchivedAt: nullTimestamp(&now), UpdatedAt: timestamp(now)})
}

// ResumeJob monitors a paused or archived job again, checking for missed runs
// from now. It reports false, changing nothing, for a job already monitored,
// including one whose pause has run out.
func (s *Store) ResumeJob(ctx context.Context, j model.Job, now time.Time) (bool, error) {
	if j.Monitored(now) {
		return false, nil
	}
	err := db.New(s.DB).ResumeJob(ctx, db.ResumeJobParams{ID: j.ID, MissedCheckedUntil: sql.NullString{String: timestamp(now), Valid: true}, UpdatedAt: timestamp(now)})
	return err == nil, err
}
