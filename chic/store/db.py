"""Durable conversation + preference store (app.db).

Reproduces the Go two-pool discipline: a single-connection **writer** engine
(SQLite allows one writer; queueing in-process beats SQLITE_BUSY) and a
multi-connection **reader** engine, both over the same WAL file. Litestream
replicates the file; the ORM choice is orthogonal to that.
"""

from __future__ import annotations

import dataclasses
import time
from pathlib import Path
from typing import Any

from alembic import command
from alembic.config import Config
from sqlalchemy import Connection, delete, event, func, select
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine

from chic.store.models import MessageRow, SessionSummaryRow, UserMemoryRow

MAX_RENDERED_PREFERENCES = 50
_MIGRATIONS_DIR = Path(__file__).parent / "migrations"


def _run_migrations(sync_connection: Connection) -> None:
    """Apply Alembic migrations to head on the store's own connection."""
    cfg = Config()
    cfg.set_main_option("script_location", str(_MIGRATIONS_DIR))
    cfg.attributes["connection"] = sync_connection
    command.upgrade(cfg, "head")


@dataclasses.dataclass(frozen=True)
class Message:
    id: int
    role: str
    content: str


@dataclasses.dataclass(frozen=True)
class Preference:
    key: str
    value: str


def _now() -> int:
    return int(time.time())


def _install_pragmas(engine: AsyncEngine) -> None:
    @event.listens_for(engine.sync_engine, "connect")
    def _set_pragmas(dbapi_conn: Any, _record: Any) -> None:
        cur = dbapi_conn.cursor()
        cur.execute("PRAGMA journal_mode=WAL")
        cur.execute("PRAGMA busy_timeout=5000")
        cur.execute("PRAGMA foreign_keys=ON")
        cur.execute("PRAGMA synchronous=NORMAL")
        cur.close()


class Store:
    """SQLite-backed durable store with single-writer discipline."""

    def __init__(self, writer: AsyncEngine, reader: AsyncEngine) -> None:
        self._writer = writer
        self._reader = reader

    @classmethod
    async def open(cls, path: str) -> Store:
        """Open (or create) the app database at path and ensure the schema."""
        url = f"sqlite+aiosqlite:///{path}"
        writer = create_async_engine(url, pool_size=1, max_overflow=0)
        reader = create_async_engine(url)
        _install_pragmas(writer)
        _install_pragmas(reader)
        async with writer.begin() as conn:
            await conn.run_sync(_run_migrations)
        return cls(writer, reader)

    async def close(self) -> None:
        await self._reader.dispose()
        await self._writer.dispose()

    # ---- messages ---------------------------------------------------------

    async def append_message(self, user_id: int, role: str, content: str) -> None:
        async with self._writer.begin() as conn:
            await conn.execute(
                sqlite_insert(MessageRow).values(
                    user_id=user_id, role=role, content=content, created_at=_now()
                )
            )

    async def start_session(self, user_id: int) -> None:
        """Draw a /new session boundary (a role='reset' sentinel row)."""
        await self.append_message(user_id, "reset", "")

    async def session_epoch(self, user_id: int) -> int:
        stmt = select(func.coalesce(func.max(MessageRow.id), 0)).where(
            MessageRow.user_id == user_id, MessageRow.role == "reset"
        )
        async with self._reader.connect() as conn:
            return int((await conn.execute(stmt)).scalar_one())

    async def recent_messages(self, user_id: int, n: int) -> list[Message]:
        """Last n turns of the current session, chronological, boundary excluded."""
        if n <= 0:
            return []
        last_reset = (
            select(func.coalesce(func.max(MessageRow.id), 0))
            .where(MessageRow.user_id == user_id, MessageRow.role == "reset")
            .scalar_subquery()
        )
        stmt = (
            select(MessageRow.id, MessageRow.role, MessageRow.content)
            .where(MessageRow.user_id == user_id, MessageRow.id > last_reset)
            .order_by(MessageRow.id.desc())
            .limit(n)
        )
        async with self._reader.connect() as conn:
            rows = list((await conn.execute(stmt)).all())
        rows.reverse()  # desc-limit then chronological
        return [Message(id=r.id, role=r.role, content=r.content) for r in rows]

    async def messages_since(self, user_id: int, since_id: int, limit: int) -> list[Message]:
        """Up to limit user/assistant turns with id > since_id (summary watermark)."""
        if limit <= 0:
            return []
        stmt = (
            select(MessageRow.id, MessageRow.role, MessageRow.content)
            .where(
                MessageRow.user_id == user_id,
                MessageRow.id > since_id,
                MessageRow.role.in_(("user", "assistant")),
            )
            .order_by(MessageRow.id.asc())
            .limit(limit)
        )
        async with self._reader.connect() as conn:
            rows = (await conn.execute(stmt)).all()
        return [Message(id=r.id, role=r.role, content=r.content) for r in rows]

    # ---- rolling summary --------------------------------------------------

    async def get_session_summary(self, user_id: int, epoch: int) -> tuple[str, int]:
        stmt = select(SessionSummaryRow.summary, SessionSummaryRow.up_to_id).where(
            SessionSummaryRow.user_id == user_id, SessionSummaryRow.epoch == epoch
        )
        async with self._reader.connect() as conn:
            row = (await conn.execute(stmt)).first()
        return (row.summary, row.up_to_id) if row is not None else ("", 0)

    async def put_session_summary(
        self, user_id: int, epoch: int, summary: str, up_to_id: int
    ) -> None:
        stmt = sqlite_insert(SessionSummaryRow).values(
            user_id=user_id, epoch=epoch, summary=summary, up_to_id=up_to_id, updated_at=_now()
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=["user_id", "epoch"],
            set_={"summary": summary, "up_to_id": up_to_id, "updated_at": _now()},
        )
        async with self._writer.begin() as conn:
            await conn.execute(stmt)

    # ---- durable preferences ----------------------------------------------

    async def set_preference(self, user_id: int, key: str, value: str) -> None:
        stmt = sqlite_insert(UserMemoryRow).values(
            user_id=user_id, key=key, value=value, updated_at=_now()
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=["user_id", "key"],
            set_={"value": value, "updated_at": _now()},
        )
        async with self._writer.begin() as conn:
            await conn.execute(stmt)

    async def delete_preference(self, user_id: int, key: str) -> None:
        async with self._writer.begin() as conn:
            await conn.execute(
                delete(UserMemoryRow).where(
                    UserMemoryRow.user_id == user_id, UserMemoryRow.key == key
                )
            )

    async def preferences(self, user_id: int) -> list[Preference]:
        """Most-recently-updated preferences (capped), ordered by key for stability."""
        recent = (
            select(UserMemoryRow.key, UserMemoryRow.value, UserMemoryRow.updated_at)
            .where(UserMemoryRow.user_id == user_id)
            .order_by(UserMemoryRow.updated_at.desc())
            .limit(MAX_RENDERED_PREFERENCES)
            .subquery()
        )
        stmt = select(recent.c.key, recent.c.value).order_by(recent.c.key.asc())
        async with self._reader.connect() as conn:
            rows = (await conn.execute(stmt)).all()
        return [Preference(key=r.key, value=r.value) for r in rows]
