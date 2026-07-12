"""Durable snapshot store (history.db), SQLite with single-writer discipline.

Mirrors :mod:`chic.store.db`: a single-connection writer engine plus a
multi-connection reader engine over one WAL file. Writes are idempotent upserts
keyed by ``(snapshot_date, product_href)``, so re-running a day's capture (after a
restart or a duplicate tick) overwrites rather than duplicates.
"""

from __future__ import annotations

import dataclasses
from datetime import datetime, timedelta
from typing import Any

from sqlalchemy import Connection, distinct, event, func, select
from sqlalchemy.dialects.sqlite import insert as sqlite_insert
from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine

from chic.history.models import Base, SalesCaptureRow, SalesSnapshotRow, StockSnapshotRow

_FMT = "%Y-%m-%d"


@dataclasses.dataclass(frozen=True)
class DemandDay:
    """One product's demand on one day (raw minor units, as stored)."""

    snapshot_date: str
    product_href: str
    name: str
    code: str
    sell_quantity: float
    sell_sum_minor: float
    sell_cost_minor: float
    profit_minor: float


@dataclasses.dataclass(frozen=True)
class Coverage:
    """Completeness of the captured sales history over its own span."""

    captured_days: int  # distinct days the job processed
    gap_days: int  # calendar days in [first, last] never captured (outage gaps)
    first: str | None
    last: str | None
    gaps: list[str]  # the missing dates (capped for display)


def _create_schema(sync_connection: Connection) -> None:
    Base.metadata.create_all(sync_connection)


def _install_pragmas(engine: AsyncEngine) -> None:
    @event.listens_for(engine.sync_engine, "connect")
    def _set_pragmas(dbapi_conn: Any, _record: Any) -> None:
        cur = dbapi_conn.cursor()
        cur.execute("PRAGMA journal_mode=WAL")
        cur.execute("PRAGMA busy_timeout=5000")
        cur.execute("PRAGMA synchronous=NORMAL")
        cur.close()


class HistoryStore:
    def __init__(self, writer: AsyncEngine, reader: AsyncEngine) -> None:
        self._writer = writer
        self._reader = reader

    @classmethod
    async def open(cls, path: str) -> HistoryStore:
        url = f"sqlite+aiosqlite:///{path}"
        writer = create_async_engine(url, pool_size=1, max_overflow=0)
        reader = create_async_engine(url)
        _install_pragmas(writer)
        _install_pragmas(reader)
        async with writer.begin() as conn:
            await conn.run_sync(_create_schema)
        return cls(writer, reader)

    async def close(self) -> None:
        await self._reader.dispose()
        await self._writer.dispose()

    # ---- writes -----------------------------------------------------------

    async def save_stock(self, date: str, rows: list[dict[str, Any]]) -> int:
        """Upsert one day's stock snapshot; returns the number of rows written."""
        return await self._upsert(StockSnapshotRow, date, rows, ["snapshot_date", "product_href"])

    async def save_sales(self, date: str, rows: list[dict[str, Any]]) -> int:
        """Upsert one day's sales snapshot; returns the number of rows written."""
        return await self._upsert(SalesSnapshotRow, date, rows, ["snapshot_date", "product_href"])

    async def mark_sales_captured(self, date: str, rows: int) -> None:
        """Record that ``date``'s sales were processed (``rows`` may be 0). Written
        for every day the job touches, so gaps in this table are true outage gaps."""
        stmt = sqlite_insert(SalesCaptureRow).values(snapshot_date=date, rows=rows)
        stmt = stmt.on_conflict_do_update(
            index_elements=["snapshot_date"], set_={"rows": stmt.excluded.rows}
        )
        async with self._writer.begin() as conn:
            await conn.execute(stmt)

    async def _upsert(
        self, model: type[Base], date: str, rows: list[dict[str, Any]], keys: list[str]
    ) -> int:
        if not rows:
            return 0
        values = [{**r, "snapshot_date": date} for r in rows]
        # Derive the ON CONFLICT update set from the schema, not from values[0]:
        # a heterogeneous chunk (a column present only in later rows) would
        # otherwise never be updated on conflict for any row.
        cols = {c.name for c in model.__table__.columns}
        update_cols = cols - set(keys)
        async with self._writer.begin() as conn:
            for chunk in _chunks(values, 500):  # stay well under SQLite's variable cap
                stmt = sqlite_insert(model).values(chunk)
                stmt = stmt.on_conflict_do_update(
                    index_elements=keys,
                    set_={k: getattr(stmt.excluded, k) for k in update_cols},
                )
                await conn.execute(stmt)
        return len(values)

    # ---- reads ------------------------------------------------------------

    async def latest_stock_date(self) -> str | None:
        stmt = select(func.max(StockSnapshotRow.snapshot_date))
        async with self._reader.connect() as conn:
            return (await conn.execute(stmt)).scalar_one_or_none()

    async def latest_sales_date(self) -> str | None:
        stmt = select(func.max(SalesSnapshotRow.snapshot_date))
        async with self._reader.connect() as conn:
            return (await conn.execute(stmt)).scalar_one_or_none()

    async def has_stock(self, date: str) -> bool:
        stmt = (
            select(StockSnapshotRow.product_href)
            .where(StockSnapshotRow.snapshot_date == date)
            .limit(1)
        )
        async with self._reader.connect() as conn:
            return (await conn.execute(stmt)).first() is not None

    async def sales_dates(self) -> list[str]:
        stmt = select(distinct(SalesSnapshotRow.snapshot_date)).order_by(
            SalesSnapshotRow.snapshot_date
        )
        async with self._reader.connect() as conn:
            return [r[0] for r in (await conn.execute(stmt)).all()]

    async def daily_demand(self, since: str | None = None) -> list[DemandDay]:
        """Per-product daily demand, oldest first — the series XYZ/purchase feed on."""
        stmt = select(
            SalesSnapshotRow.snapshot_date,
            SalesSnapshotRow.product_href,
            SalesSnapshotRow.name,
            SalesSnapshotRow.code,
            SalesSnapshotRow.sell_quantity,
            SalesSnapshotRow.sell_sum_minor,
            SalesSnapshotRow.sell_cost_minor,
            SalesSnapshotRow.profit_minor,
        ).order_by(SalesSnapshotRow.snapshot_date, SalesSnapshotRow.product_href)
        if since is not None:
            stmt = stmt.where(SalesSnapshotRow.snapshot_date >= since)
        async with self._reader.connect() as conn:
            rows = (await conn.execute(stmt)).all()
        return [
            DemandDay(
                snapshot_date=r.snapshot_date,
                product_href=r.product_href,
                name=r.name,
                code=r.code,
                sell_quantity=r.sell_quantity,
                sell_sum_minor=r.sell_sum_minor,
                sell_cost_minor=r.sell_cost_minor,
                profit_minor=r.profit_minor,
            )
            for r in rows
        ]

    async def sales_day_count(self) -> int:
        stmt = select(func.count(distinct(SalesSnapshotRow.snapshot_date)))
        async with self._reader.connect() as conn:
            return int((await conn.execute(stmt)).scalar_one())

    async def sales_coverage(self, *, max_gaps: int = 10) -> Coverage:
        """Completeness of the captured sales history: which calendar days between
        the first and last capture were never processed (i.e. real outage gaps, as
        opposed to legitimately zero/closed days, which are still marked captured)."""
        stmt = select(SalesCaptureRow.snapshot_date).order_by(SalesCaptureRow.snapshot_date)
        async with self._reader.connect() as conn:
            dates = [r[0] for r in (await conn.execute(stmt)).all()]
        if not dates:
            return Coverage(captured_days=0, gap_days=0, first=None, last=None, gaps=[])
        captured = set(dates)
        first, last = dates[0], dates[-1]
        gaps: list[str] = []
        day = datetime.strptime(first, _FMT).date()
        end = datetime.strptime(last, _FMT).date()
        while day <= end:
            s = day.strftime(_FMT)
            if s not in captured:
                gaps.append(s)
            day += timedelta(days=1)
        return Coverage(
            captured_days=len(captured),
            gap_days=len(gaps),
            first=first,
            last=last,
            gaps=gaps[:max_gaps],
        )


def _chunks[T](items: list[T], size: int) -> list[list[T]]:
    return [items[i : i + size] for i in range(0, len(items), size)]
