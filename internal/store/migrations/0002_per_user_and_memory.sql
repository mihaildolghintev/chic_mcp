-- +goose Up
-- The bot is private 1-on-1, where Telegram sets chat_id == user_id, so an old
-- chat_id is a valid user_id: renaming the column preserves every stored dialog
-- (no table rebuild, no data copy). user_memory holds durable per-user
-- preferences that outlive a /new session reset.
ALTER TABLE messages RENAME COLUMN chat_id TO user_id;
DROP INDEX IF EXISTS idx_messages_chat;
CREATE INDEX IF NOT EXISTS idx_messages_user ON messages(user_id, id);

CREATE TABLE IF NOT EXISTS user_memory (
	user_id    INTEGER NOT NULL,
	key        TEXT    NOT NULL,
	value      TEXT    NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, key)
);

-- +goose Down
DROP TABLE user_memory;
DROP INDEX IF EXISTS idx_messages_user;
ALTER TABLE messages RENAME COLUMN user_id TO chat_id;
CREATE INDEX idx_messages_chat ON messages(chat_id, id);
