package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB      *sql.DB
	DataDir string
}

func Open(ctx context.Context, dataDir string) (*Store, error) {
	// The database is opened by file: URL, where a relative path would be
	// read as a host name.
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	// Only tighten permissions on a directory CronWatch creates; an existing
	// directory may be shared and is the user's to manage.
	if _, err := os.Stat(dataDir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dataDir, 0o700); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	path := filepath.Join(dataDir, "cronwatch.db")
	// WAL mode is persistent in the database file, so Migrate enables it once
	// under the migration lock. Requesting it on every connection races when
	// several processes open a new database and fails with SQLITE_BUSY.
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"}).String()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure database permissions: %w", err)
	}

	s := &Store{DB: db, DataDir: dataDir}
	if err := s.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) Close() error {
	return s.DB.Close()
}
