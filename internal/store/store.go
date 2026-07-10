// Package store persists conversation history in app.db — the durable
// system-of-record database, deliberately separate from the regenerable
// cache.db. Losing cache.db costs warm-up requests; losing app.db loses
// dialog memory, so this file lives on the mounted volume and is the one
// Litestream will replicate.
//
// Two kinds of memory live here, with different lifetimes:
//   - messages: the working dialog history, keyed by Telegram user. It is
//     replayed into the LLM context and reset by /new (a session boundary).
//   - user_memory: durable per-user preferences (language, reply style,
//     business specifics). It survives /new and every session — the bot's
//     long-term memory of who it is talking to.
//
// The bot is private and never in group chats, so a Telegram user always maps
// to exactly one conversation; user_id alone is the key (in a private chat
// chat_id == user_id anyway).
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// migrationsFS embeds the SQL migrations so the binary carries its own schema
// history — no files to ship alongside it. goose applies them in order and
// records progress in its own goose_db_version table.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Message is one turn of a chat dialog, as the agent replays it to the LLM.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Preference is one durable fact the bot remembers about a user — a stable
// key ("language", "reply_style") and its value.
type Preference struct {
	Key   string
	Value string
}

// Store is the persistence seam the agent depends on. An interface so agent
// tests can run against a fake without touching the filesystem.
type Store interface {
	AppendMessage(ctx context.Context, userID int64, role, content string) error
	RecentMessages(ctx context.Context, userID int64, n int) ([]Message, error)
	StartSession(ctx context.Context, userID int64) error

	SetPreference(ctx context.Context, userID int64, key, value string) error
	DeletePreference(ctx context.Context, userID int64, key string) error
	Preferences(ctx context.Context, userID int64) ([]Preference, error)
}

// DB is the SQLite-backed Store. Writes go through a dedicated single-connection
// pool (SQLite allows one writer at a time; queueing in Go beats ErrBusy),
// reads through a separate multi-connection pool.
type DB struct {
	write *sql.DB
	read  *sql.DB
}

// The same pragmas the cache uses, plus foreign_keys and synchronous=NORMAL
// (durability with WAL at a fraction of FULL's fsync cost).
const dsnPragmas = "?_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(WAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)"

// Open opens (or creates) the app database at path and applies migrations.
// ":memory:" is not supported — the two pools would each get a private
// in-memory database; tests should use a file in t.TempDir().
func Open(path string) (*DB, error) {
	write, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
	if err != nil {
		return nil, fmt.Errorf("store: open write pool: %w", err)
	}
	write.SetMaxOpenConns(1)
	if err := migrate(write); err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}

	read, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: open read pool: %w", err)
	}
	return &DB{write: write, read: read}, nil
}

// newProvider builds the goose provider over the embedded migrations. It is the
// single source of migration configuration — both startup (migrate) and tests
// go through it, so they can never drift apart.
func newProvider(db *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return nil, fmt.Errorf("store: goose provider: %w", err)
	}
	return provider, nil
}

// migrate applies every embedded migration in order via goose. It is
// idempotent — already-applied migrations are skipped — so it runs safely on
// every startup, on fresh and existing databases alike. Databases that pre-date
// goose adoption already carry the 0001 schema; goose's first migration uses
// IF NOT EXISTS, so adopting it costs nothing and 0002 upgrades them in place.
func migrate(db *sql.DB) error {
	provider, err := newProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("store: apply migrations: %w", err)
	}
	return nil
}

// Close closes both pools.
func (d *DB) Close() error {
	rerr := d.read.Close()
	werr := d.write.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// AppendMessage records one dialog turn for userID.
func (d *DB) AppendMessage(ctx context.Context, userID int64, role, content string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO messages (user_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		userID, role, content, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: append message: %w", err)
	}
	return nil
}

// StartSession draws a session boundary for userID: RecentMessages stops
// replaying anything said before it. Old rows stay in the table — app.db is
// the system of record — only the agent's working memory is reset. Durable
// preferences in user_memory are untouched.
func (d *DB) StartSession(ctx context.Context, userID int64) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO messages (user_id, role, content, created_at) VALUES (?, 'reset', '', ?)`,
		userID, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: start session: %w", err)
	}
	return nil
}

// RecentMessages returns the last n messages of the current session for
// userID in chronological order — ready to be prepended to an LLM
// conversation. Messages before the last session boundary (and the boundary
// rows themselves) are never returned.
func (d *DB) RecentMessages(ctx context.Context, userID int64, n int) ([]Message, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := d.read.QueryContext(ctx,
		`SELECT role, content FROM (
			SELECT id, role, content FROM messages
			WHERE user_id = ?
			  AND id > COALESCE((SELECT MAX(id) FROM messages WHERE user_id = ? AND role = 'reset'), 0)
			ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`,
		userID, userID, n,
	)
	if err != nil {
		return nil, fmt.Errorf("store: recent messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Role, &m.Content); err != nil {
			return nil, fmt.Errorf("store: scan message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate messages: %w", err)
	}
	return out, nil
}

// SetPreference stores (or overwrites) a durable preference for userID. Keys
// are stable identifiers the agent chooses ("language", "reply_style"); a
// repeated key updates the value in place.
func (d *DB) SetPreference(ctx context.Context, userID int64, key, value string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO user_memory (user_id, key, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		userID, key, value, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: set preference: %w", err)
	}
	return nil
}

// DeletePreference removes one preference for userID. Deleting a missing key is
// not an error — the postcondition (the key is absent) already holds.
func (d *DB) DeletePreference(ctx context.Context, userID int64, key string) error {
	_, err := d.write.ExecContext(ctx,
		`DELETE FROM user_memory WHERE user_id = ? AND key = ?`, userID, key,
	)
	if err != nil {
		return fmt.Errorf("store: delete preference: %w", err)
	}
	return nil
}

// maxRenderedPreferences bounds how many preferences Preferences returns — the
// whole profile is injected into every system prompt, so an accumulation of
// keys must never grow it without limit. The most recently updated survive;
// beyond this many the user should prune with forget_preference. It is a safety
// ceiling, well above any realistic profile, not a UX target.
const maxRenderedPreferences = 50

// Preferences returns the user's stored preferences, capped at
// maxRenderedPreferences most-recently-updated and ordered by key for a stable
// prompt rendering.
func (d *DB) Preferences(ctx context.Context, userID int64) ([]Preference, error) {
	rows, err := d.read.QueryContext(ctx,
		`SELECT key, value FROM (
			SELECT key, value, updated_at FROM user_memory
			WHERE user_id = ?
			ORDER BY updated_at DESC
			LIMIT ?
		) ORDER BY key ASC`,
		userID, maxRenderedPreferences,
	)
	if err != nil {
		return nil, fmt.Errorf("store: preferences: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.Key, &p.Value); err != nil {
			return nil, fmt.Errorf("store: scan preference: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate preferences: %w", err)
	}
	return out, nil
}
