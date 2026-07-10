-- +goose Up
-- The first released schema: dialog history keyed on chat_id. IF NOT EXISTS
-- makes this a safe no-op on databases that pre-date goose adoption (their
-- messages table already exists; goose just records the version).
CREATE TABLE IF NOT EXISTS messages (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_id    INTEGER NOT NULL,
	role       TEXT    NOT NULL,
	content    TEXT    NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages(chat_id, id);

-- +goose Down
DROP TABLE messages;
