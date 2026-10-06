package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// metaMaintained is the meta key for when missed runs were last checked.
const metaMaintained = "maintained_at"

// SetMaintained records that missed runs were checked at t.
func (s *Store) SetMaintained(ctx context.Context, t time.Time) error {
	return db.New(s.DB).SetMeta(ctx, db.SetMetaParams{Key: metaMaintained, Value: timestamp(t)})
}

// Maintained returns when missed runs were last checked, or nil if never.
func (s *Store) Maintained(ctx context.Context) (*time.Time, error) {
	v, err := db.New(s.DB).GetMeta(ctx, metaMaintained)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t, err := parseTime(v)
	return &t, err
}
