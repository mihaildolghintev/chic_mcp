// Package store persists conversation history in app.db — the durable
// system-of-record database, deliberately separate from the regenerable
// cache.db. Losing cache.db costs warm-up requests; losing app.db loses
// dialog memory, so this file lives on the mounted volume and is the one
// Litestream will replicate.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Message is one turn of a chat dialog, as the agent replays it to the LLM.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Store is the persistence seam the agent depends on. An interface so agent
// tests can run against a fake without touching the filesystem.
type Store interface {
	AppendMessage(ctx context.Context, chatID int64, role, content string) error
	RecentMessages(ctx context.Context, chatID int64, n int) ([]Message, error)
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

const schema = `
CREATE TABLE IF NOT EXISTS messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id    INTEGER NOT NULL,
	role       TEXT    NOT NULL,
	content    TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, id);
`

// Open opens (or creates) the app database at path and applies migrations.
// ":memory:" is not supported — the two pools would each get a private
// in-memory database; tests should use a file in t.TempDir().
func Open(path string) (*DB, error) {
	write, err := sql.Open("sqlite", "file:"+path+dsnPragmas)
	if err != nil {
		return nil, fmt.Errorf("store: open write pool: %w", err)
	}
	write.SetMaxOpenConns(1)
	if _, err := write.Exec(schema); err != nil {
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

// Close closes both pools.
func (d *DB) Close() error {
	rerr := d.read.Close()
	werr := d.write.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// AppendMessage records one dialog turn for chatID.
func (d *DB) AppendMessage(ctx context.Context, chatID int64, role, content string) error {
	_, err := d.write.ExecContext(ctx,
		`INSERT INTO messages (chat_id, role, content, created_at) VALUES (?, ?, ?, ?)`,
		chatID, role, content, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: append message: %w", err)
	}
	return nil
}

// RecentMessages returns the last n messages for chatID in chronological
// order — ready to be prepended to an LLM conversation.
func (d *DB) RecentMessages(ctx context.Context, chatID int64, n int) ([]Message, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := d.read.QueryContext(ctx,
		`SELECT role, content FROM (
			SELECT id, role, content FROM messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?
		) ORDER BY id ASC`,
		chatID, n,
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
