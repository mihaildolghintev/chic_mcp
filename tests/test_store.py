from __future__ import annotations

import sqlite3
from collections.abc import AsyncIterator
from pathlib import Path

import pytest_asyncio
from chic.store import Store

UID = 42


async def test_open_adopts_pre_alembic_go_db(tmp_path: Path) -> None:
    """A DB created by the Go/goose build (tables, no alembic_version) is adopted,
    not re-created — startup must not crash and existing history is preserved."""
    path = str(tmp_path / "app.db")
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL,
            role TEXT, content TEXT, created_at INTEGER);
        CREATE INDEX idx_messages_user ON messages(user_id, id);
        CREATE TABLE user_memory (user_id INTEGER, key TEXT, value TEXT, updated_at INTEGER,
            PRIMARY KEY(user_id, key));
        CREATE TABLE session_summary (user_id INTEGER, epoch INTEGER, summary TEXT,
            up_to_id INTEGER, updated_at INTEGER, PRIMARY KEY(user_id, epoch));
        """
    )
    conn.execute(
        "INSERT INTO messages (user_id, role, content, created_at) VALUES (?,?,?,?)",
        (UID, "user", "старое сообщение", 1),
    )
    conn.commit()
    conn.close()

    store = await Store.open(path)
    try:
        history = await store.recent_messages(UID, 10)
        assert any(m.content == "старое сообщение" for m in history)  # preserved
        await store.append_message(UID, "assistant", "новый ответ")  # writes still work
        assert (await store.recent_messages(UID, 10))[-1].content == "новый ответ"
    finally:
        await store.close()


@pytest_asyncio.fixture
async def store(tmp_path: Path) -> AsyncIterator[Store]:
    s = await Store.open(str(tmp_path / "app.db"))
    try:
        yield s
    finally:
        await s.close()


async def test_append_and_recent(store: Store) -> None:
    await store.append_message(UID, "user", "hi")
    await store.append_message(UID, "assistant", "hello")
    msgs = await store.recent_messages(UID, 10)
    assert [(m.role, m.content) for m in msgs] == [("user", "hi"), ("assistant", "hello")]


async def test_session_boundary_resets_recent(store: Store) -> None:
    await store.append_message(UID, "user", "old")
    epoch_before = await store.session_epoch(UID)
    await store.start_session(UID)
    epoch_after = await store.session_epoch(UID)
    assert epoch_after > epoch_before

    await store.append_message(UID, "user", "new")
    msgs = await store.recent_messages(UID, 10)
    # Only the post-boundary turn survives; the reset row itself is excluded.
    assert [m.content for m in msgs] == ["new"]


async def test_messages_since_watermark(store: Store) -> None:
    await store.append_message(UID, "user", "a")
    await store.append_message(UID, "assistant", "b")
    first = await store.recent_messages(UID, 10)
    watermark = first[0].id
    later = await store.messages_since(UID, watermark, 10)
    assert [m.content for m in later] == ["b"]  # only rows after the watermark


async def test_session_summary_upsert(store: Store) -> None:
    summary, up_to = await store.get_session_summary(UID, epoch=0)
    assert summary == ""
    assert up_to == 0

    await store.put_session_summary(UID, 0, "recap v1", 5)
    await store.put_session_summary(UID, 0, "recap v2", 9)  # upsert
    summary, up_to = await store.get_session_summary(UID, 0)
    assert summary == "recap v2"
    assert up_to == 9


async def test_preferences_set_get_delete(store: Store) -> None:
    await store.set_preference(UID, "language", "ru")
    await store.set_preference(UID, "reply_style", "short")
    await store.set_preference(UID, "language", "en")  # overwrite

    prefs = {p.key: p.value for p in await store.preferences(UID)}
    assert prefs == {"language": "en", "reply_style": "short"}

    await store.delete_preference(UID, "reply_style")
    await store.delete_preference(UID, "missing")  # no-op, not an error
    prefs = {p.key: p.value for p in await store.preferences(UID)}
    assert prefs == {"language": "en"}


async def test_preferences_ordered_by_key(store: Store) -> None:
    await store.set_preference(UID, "zeta", "1")
    await store.set_preference(UID, "alpha", "2")
    keys = [p.key for p in await store.preferences(UID)]
    assert keys == ["alpha", "zeta"]  # stable prompt rendering
