"""Capture stock + sales into history.db, backfilling any missing days.

Idempotent. Stock is recorded as-of the run (a point-in-time level, today only —
past levels aren't used by the demand statistics). Sales are recorded per complete
day: every day from just after the last stored day (or a seed floor) through
yesterday, so an outage — or a fresh deploy — is caught up from the live API in one
pass, keeping the demand series continuous. The window is capped so a long gap (or
first run) can't hammer the API; a fully-zero-sales day writes no row and is treated
as a gap (a closed day is "no opportunity", not "zero demand").
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from datetime import datetime, timedelta
from typing import Any

from chic.aggregate.report import product_key
from chic.cache import Source
from chic.clock import now as _now
from chic.history.store import HistoryStore
from chic.moysklad import ProfitOptions, StockOptions
from chic.moysklad.models import ProfitByProductRow, StockRow

logger = logging.getLogger(__name__)

_FMT = "%Y-%m-%d"


def _stock_row(r: StockRow) -> dict[str, Any]:
    return {
        "product_href": product_key(r.meta.href, r.name),
        "name": r.name,
        "code": r.code,
        "stock": r.stock,
        "reserve": r.reserve,
        "in_transit": r.in_transit,
        "price_minor": r.price,
        "sale_price_minor": r.sale_price,
        "stock_days": int(r.stock_days),
    }


def _sales_row(r: ProfitByProductRow) -> dict[str, Any]:
    return {
        "product_href": product_key(r.assortment.meta.href, r.assortment.name),
        "name": r.assortment.name,
        "code": r.assortment.code,
        "sell_quantity": r.sell_quantity,
        "sell_sum_minor": r.sell_sum,
        "sell_cost_minor": r.sell_cost_sum,
        "profit_minor": r.profit,
        "return_quantity": r.return_quantity,
        "return_sum_minor": r.return_sum,
    }


def _dedupe(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Collapse duplicate product_href within one snapshot (last wins, matching upsert)."""
    by_key: dict[str, dict[str, Any]] = {}
    for row in rows:
        by_key[row["product_href"]] = row
    return list(by_key.values())


class SnapshotService:
    def __init__(
        self,
        api: Source,
        history: HistoryStore,
        *,
        clock: Callable[[], datetime] = _now,  # account-local wall clock, not naive UTC
    ) -> None:
        self._api = api
        self._history = history
        self._clock = clock

    async def capture(self, *, max_backfill_days: int) -> int:
        """Snapshot today's stock and backfill every missing sales day in the window.

        Idempotent: today's stock is skipped if already stored, and sales are fetched
        only for days not yet present. Returns the number of rows written.
        """
        now = self._clock()
        today = now.strftime(_FMT)
        written = 0

        if not await self._history.has_stock(today):
            written += await self._capture_stock(today)

        last = await self._history.latest_sales_date()
        for day in _sales_days(now, max_backfill_days, last):
            written += await self._capture_sales(day)
        return written

    async def _capture_stock(self, date: str) -> int:
        rows = await self._api.get_stock(
            StockOptions(stock_mode="positiveOnly", group_by="product")
        )
        n = await self._history.save_stock(date, _dedupe([_stock_row(r) for r in rows]))
        logger.info("snapshot: %d stock rows @ %s", n, date)
        return n

    async def _capture_sales(self, date: str) -> int:
        rows = await self._api.profit_by_product(False, ProfitOptions(from_=date, to=date))
        n = await self._history.save_sales(date, _dedupe([_sales_row(r) for r in rows]))
        # Mark the day processed even when n == 0, so a zero/closed day is recorded as
        # captured (not a gap) — the whole point of distinguishing gaps from zeros.
        await self._history.mark_sales_captured(date, n)
        logger.info("snapshot: %d sales rows @ %s", n, date)
        return n


def _sales_days(now: datetime, max_backfill_days: int, last: str | None) -> list[str]:
    """Complete days that still need a sales snapshot: from just after ``last`` (or
    the seed floor) through yesterday, capped to ``max_backfill_days`` days."""
    n = max(max_backfill_days, 1)
    end = (now - timedelta(days=1)).date()  # yesterday — the last complete day
    floor = end - timedelta(days=n - 1)
    if last is None:
        start = floor
    else:
        start = max(datetime.strptime(last, _FMT).date() + timedelta(days=1), floor)
    days: list[str] = []
    day = start
    while day <= end:
        days.append(day.strftime(_FMT))
        day += timedelta(days=1)
    return days
