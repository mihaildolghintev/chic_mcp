-- +goose Up
-- session_summary holds the rolling recap of one dialog session, keyed by the
-- session's epoch (the id of its /new-or-idle boundary; 0 for the pre-first-reset
-- session). The summary is folded incrementally: up_to_id records the last
-- message already condensed, so each request only summarizes the new tail
-- instead of re-summarizing the whole history. A new epoch (from /new or an idle
-- timeout) simply gets a fresh row, so the old summary is naturally abandoned.
CREATE TABLE IF NOT EXISTS session_summary (
	user_id    INTEGER NOT NULL,
	epoch      INTEGER NOT NULL,
	summary    TEXT    NOT NULL,
	up_to_id   INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (user_id, epoch)
);

-- +goose Down
DROP TABLE session_summary;
