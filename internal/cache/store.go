// Package cache provides a persistent, CGO-free (modernc.org/sqlite) TTL cache
// for MoySklad API responses. It exists so repeated or similar analytical
// questions do not hit MoySklad on every call and press against the account
// rate limit (45 requests / 3 seconds). Cached values are exactly what MoySklad
// returned — no report math is recomputed locally.
package cache

import (
	"database/sql"
	"errors"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed key/value cache with per-entry expiry.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// The cache is best-effort: failures are logged, never propagated to callers.
func (s *Store) log() *slog.Logger { return slog.Default() }

const schema = `
CREATE TABLE IF NOT EXISTS cache (
	key        TEXT PRIMARY KEY,
	value      BLOB NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_expires ON cache(expires_at);
`

// OpenStore opens (or creates) the cache database at path. Use ":memory:" for
// an ephemeral cache. WAL + a busy timeout keep concurrent reads/writes smooth.
func OpenStore(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Single writer avoids "database is locked" on this low-traffic service.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, now: time.Now}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Get returns the unexpired value for key, or ok=false on miss/expiry.
func (s *Store) Get(key string) ([]byte, bool) {
	var value []byte
	err := s.db.QueryRow(
		`SELECT value FROM cache WHERE key = ? AND expires_at > ?`,
		key, s.now().Unix(),
	).Scan(&value)
	if err != nil {
		// A miss is the expected path; only a real query failure is worth logging.
		if !errors.Is(err, sql.ErrNoRows) {
			s.log().Warn("cache get failed", "err", err)
		}
		return nil, false
	}
	return value, true
}

// Set stores value under key with the given TTL. A non-positive TTL is a no-op.
func (s *Store) Set(key string, value []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	expires := s.now().Add(ttl).Unix()
	if _, err := s.db.Exec(
		`INSERT INTO cache (key, value, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, expires_at = excluded.expires_at`,
		key, value, expires,
	); err != nil {
		s.log().Warn("cache set failed", "err", err)
	}
}

// Purge deletes expired entries and returns the number removed.
func (s *Store) Purge() int64 {
	res, err := s.db.Exec(`DELETE FROM cache WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		s.log().Warn("cache purge failed", "err", err)
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// StartJanitor periodically purges expired entries until the returned stop
// function is called.
func (s *Store) StartJanitor(every time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if n := s.Purge(); n > 0 {
					s.log().Debug("cache janitor purged expired entries", "count", n)
				}
			}
		}
	}()
	return func() { close(done) }
}
