package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/yeboahd24/cronwatch/migrations"
)

// Migrate serializes schema changes across CronWatch processes, then applies
// pending embedded goose migrations.
func (s *Store) Migrate(ctx context.Context) error {
	lock, err := os.OpenFile(filepath.Join(s.DataDir, "cronwatch.migrate.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	provider, err := goose.NewProvider(goose.DialectSQLite3, s.DB, migrations.FS)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}
