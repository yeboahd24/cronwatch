package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/yeboahd24/cronwatch/internal/db"
)

// Host is a server that reports to this one as a hub.
type Host struct {
	ID         string
	Name       string
	CreatedAt  time.Time
	ReportedAt *time.Time // nil until its first report
	Report     []byte     // the latest report, as JSON; nil until the first
}

// ErrNoHost means no host has the name or token.
var ErrNoHost = errors.New("no such host")

// tokenPrefix marks CronWatch hub tokens, so a leaked one is recognizable.
const tokenPrefix = "cwh_"

// newToken returns a random token and the hash stored for it.
func newToken() (token, hash string, err error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateHost adds a host and returns its token. Only the token's hash is
// stored, so this is the only time the token can be shown.
func (s *Store) CreateHost(ctx context.Context, name string, now time.Time) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	err = db.New(s.DB).CreateHost(ctx, db.CreateHostParams{ID: id, Name: name, TokenHash: hash, CreatedAt: timestamp(now)})
	return token, err
}

// NewHostToken replaces a host's token and returns the new one. The old
// token stops working at once; the host's report is kept.
func (s *Store) NewHostToken(ctx context.Context, name string) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	n, err := db.New(s.DB).SetHostToken(ctx, db.SetHostTokenParams{Name: name, TokenHash: hash})
	if err == nil && n == 0 {
		err = ErrNoHost
	}
	return token, err
}

// DeleteHost removes a host and its report; its token stops working.
func (s *Store) DeleteHost(ctx context.Context, name string) error {
	n, err := db.New(s.DB).DeleteHost(ctx, name)
	if err == nil && n == 0 {
		err = ErrNoHost
	}
	return err
}

// HostByToken returns the host a token was issued to.
func (s *Store) HostByToken(ctx context.Context, token string) (Host, error) {
	row, err := db.New(s.DB).GetHostByTokenHash(ctx, hashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNoHost
	}
	if err != nil {
		return Host{}, err
	}
	return convertHost(row)
}

// HostByName returns the named host.
func (s *Store) HostByName(ctx context.Context, name string) (Host, error) {
	row, err := db.New(s.DB).GetHostByName(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNoHost
	}
	if err != nil {
		return Host{}, err
	}
	return convertHost(row)
}

// ListHosts returns every host, by name.
func (s *Store) ListHosts(ctx context.Context) ([]Host, error) {
	rows, err := db.New(s.DB).ListHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(rows))
	for _, row := range rows {
		h, err := convertHost(row)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// SaveHostReport replaces the host's report with report, received at at.
func (s *Store) SaveHostReport(ctx context.Context, id string, at time.Time, report []byte) error {
	return db.New(s.DB).SaveHostReport(ctx, db.SaveHostReportParams{ID: id, ReportedAt: nullTimestamp(&at),
		Report: sql.NullString{String: string(report), Valid: true}})
}

func convertHost(row db.Host) (Host, error) {
	h := Host{ID: row.ID, Name: row.Name}
	var err error
	if h.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return h, err
	}
	if h.ReportedAt, err = parseNullTime(row.ReportedAt); err != nil {
		return h, err
	}
	if row.Report.Valid {
		h.Report = []byte(row.Report.String)
	}
	return h, nil
}
