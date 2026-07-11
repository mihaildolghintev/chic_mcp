"""Persistent TTL cache for MoySklad responses (separate, regenerable SQLite DB).

Cached values are exactly what MoySklad returned — no report math is recomputed
locally. The cache is best-effort: failures are logged, never propagated.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
import time
from collections.abc import Awaitable, Callable

import aiosqlite

logger = logging.getLogger(__name__)

_SCHEMA = """
CREATE TABLE IF NOT EXISTS cache (
    key        TEXT PRIMARY KEY,
    value      BLOB NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_expires ON cache(expires_at);
"""

NowFn = Callable[[], float]


class CacheStore:
    """A SQLite-backed key/value cache with per-entry expiry."""

    def __init__(self, conn: aiosqlite.Connection, now: NowFn) -> None:
        self._conn = conn
        self._now = now

    @classmethod
    async def open(cls, path: str, *, now: NowFn = time.time) -> CacheStore:
        conn = await aiosqlite.connect(path)
        await conn.execute("PRAGMA journal_mode=WAL")
        await conn.execute("PRAGMA busy_timeout=5000")
        await conn.executescript(_SCHEMA)
        await conn.commit()
        return cls(conn, now)

    async def close(self) -> None:
        await self._conn.close()

    async def get(self, key: str) -> bytes | None:
        try:
            async with self._conn.execute(
                "SELECT value FROM cache WHERE key = ? AND expires_at > ?",
                (key, int(self._now())),
            ) as cur:
                row = await cur.fetchone()
        except Exception:
            logger.warning("cache get failed", exc_info=True)
            return None
        if row is None:
            return None
        value: bytes = row[0]
        return value

    async def set(self, key: str, value: bytes, ttl: float) -> None:
        if ttl <= 0:
            return
        expires = int(self._now() + ttl)
        try:
            await self._conn.execute(
                "INSERT INTO cache (key, value, expires_at) VALUES (?, ?, ?) "
                "ON CONFLICT(key) DO UPDATE SET value = excluded.value, "
                "expires_at = excluded.expires_at",
                (key, value, expires),
            )
            await self._conn.commit()
        except Exception:
            logger.warning("cache set failed", exc_info=True)

    async def purge(self) -> int:
        try:
            cur = await self._conn.execute(
                "DELETE FROM cache WHERE expires_at <= ?", (int(self._now()),)
            )
            await self._conn.commit()
            return cur.rowcount
        except Exception:
            logger.warning("cache purge failed", exc_info=True)
            return 0

    def start_janitor(self, every: float) -> Callable[[], Awaitable[None]]:
        """Periodically purge expired entries until the returned stop() is awaited."""
        task = asyncio.create_task(self._janitor_loop(every))

        async def stop() -> None:
            task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await task

        return stop

    async def _janitor_loop(self, every: float) -> None:
        while True:
            await asyncio.sleep(every)
            removed = await self.purge()
            if removed > 0:
                logger.debug("cache janitor purged %d expired entries", removed)
