from __future__ import annotations

from datetime import datetime
from pathlib import Path
from typing import cast

import pytest_asyncio
from chic.cache import Source
from chic.history import HistoryStore, SnapshotService
from chic.moysklad import ProfitOptions, StockOptions
from chic.moysklad.models import Meta, NamedRef, ProfitByProductRow, StockRow


@pytest_asyncio.fixture
async def history(tmp_path: Path):  # type: ignore[no-untyped-def]
    h = await HistoryStore.open(str(tmp_path / "history.db"))
    try:
        yield h
    finally:
        await h.close()


def _stock_row(href: str, name: str, stock: float) -> dict[str, object]:
    return {
        "product_href": href,
        "name": name,
        "code": "",
        "stock": stock,
        "reserve": 0.0,
        "in_transit": 0.0,
        "price_minor": 5000.0,
        "sale_price_minor": 9000.0,
        "stock_days": 10,
    }


def _sales(href: str, qty: float) -> dict[str, object]:
    return {
        "product_href": href,
        "name": href,
        "code": "",
        "sell_quantity": qty,
        "sell_sum_minor": qty * 1000,
        "sell_cost_minor": qty * 600,
        "profit_minor": qty * 400,
        "return_quantity": 0.0,
        "return_sum_minor": 0.0,
    }


async def test_save_stock_is_idempotent_upsert(history: HistoryStore) -> None:
    await history.save_stock("2026-07-10", [_stock_row("h1", "Widget", 10.0)])
    # Re-run the same day with a changed level: overwrite, not duplicate.
    await history.save_stock("2026-07-10", [_stock_row("h1", "Widget", 7.0)])

    assert await history.has_stock("2026-07-10") is True
    assert await history.latest_stock_date() == "2026-07-10"
    # Later date wins for latest.
    await history.save_stock("2026-07-11", [_stock_row("h1", "Widget", 5.0)])
    assert await history.latest_stock_date() == "2026-07-11"


async def test_daily_demand_is_ordered_and_filtered(history: HistoryStore) -> None:
    def sales(href: str, qty: float) -> dict[str, object]:
        return {
            "product_href": href,
            "name": href,
            "code": "",
            "sell_quantity": qty,
            "sell_sum_minor": qty * 1000,
            "sell_cost_minor": qty * 600,
            "profit_minor": qty * 400,
            "return_quantity": 0.0,
            "return_sum_minor": 0.0,
        }

    await history.save_sales("2026-07-08", [sales("h1", 3)])
    await history.save_sales("2026-07-09", [sales("h1", 5), sales("h2", 2)])

    all_days = await history.daily_demand()
    assert [(d.snapshot_date, d.product_href) for d in all_days] == [
        ("2026-07-08", "h1"),
        ("2026-07-09", "h1"),
        ("2026-07-09", "h2"),
    ]
    recent = await history.daily_demand(since="2026-07-09")
    assert {d.snapshot_date for d in recent} == {"2026-07-09"}
    assert await history.sales_day_count() == 2


async def test_sales_coverage_distinguishes_gaps_from_zero_days(history: HistoryStore) -> None:
    # 07-02 was processed but had zero sales (still captured); 07-03/04 were never
    # processed (an outage). Only the latter are real gaps.
    for day, n in (("2026-07-01", 2), ("2026-07-02", 0), ("2026-07-05", 3)):
        await history.mark_sales_captured(day, n)

    cov = await history.sales_coverage()
    assert cov.captured_days == 3
    assert (cov.first, cov.last) == ("2026-07-01", "2026-07-05")
    assert cov.gaps == ["2026-07-03", "2026-07-04"]  # the zero-sales 07-02 is NOT a gap
    assert cov.gap_days == 2


async def test_sales_coverage_empty_history(history: HistoryStore) -> None:
    cov = await history.sales_coverage()
    assert (cov.captured_days, cov.gap_days) == (0, 0)
    assert (cov.first, cov.last, cov.gaps) == (None, None, [])


class _FakeApi:
    def __init__(self) -> None:
        self.stock_calls = 0
        self.profit_calls: list[ProfitOptions] = []

    async def get_stock(self, opts: StockOptions) -> list[StockRow]:
        self.stock_calls += 1
        return [
            StockRow(meta=Meta(href="h1"), name="Widget", stock=10.0, price=5000.0),
            StockRow(meta=Meta(href="h1"), name="Widget", stock=4.0, price=5000.0),  # dup href
        ]

    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]:
        self.profit_calls.append(opts)
        return [
            ProfitByProductRow(
                assortment=NamedRef(meta=Meta(href="h1"), name="Widget"),
                sell_quantity=6.0,
                sell_sum=60000.0,
                profit=24000.0,
            )
        ]


def _svc(api: _FakeApi, history: HistoryStore, day: str) -> SnapshotService:
    at = datetime.strptime(day, "%Y-%m-%d").replace(hour=3)
    return SnapshotService(cast(Source, api), history, clock=lambda: at)


async def test_capture_seeds_backfill_on_empty_history(history: HistoryStore) -> None:
    api = _FakeApi()
    # Empty history + a 3-day window ⇒ seed the last 3 complete days (ending yesterday).
    await _svc(api, history, "2026-07-10").capture(max_backfill_days=3)

    assert await history.has_stock("2026-07-10") is True  # stock stored under today
    dates = {opts.from_ for opts in api.profit_calls}
    assert dates == {"2026-07-07", "2026-07-08", "2026-07-09"}
    assert {d.snapshot_date for d in await history.daily_demand()} == dates


async def test_capture_marks_every_backfilled_day_no_gaps(history: HistoryStore) -> None:
    api = _FakeApi()
    await _svc(api, history, "2026-07-10").capture(max_backfill_days=3)

    # The three backfilled days (07-07..07-09) are all marked captured, contiguously.
    cov = await history.sales_coverage()
    assert cov.captured_days == 3
    assert cov.gaps == []  # contiguous span ⇒ no outage gaps


async def test_capture_fills_only_the_gap(history: HistoryStore) -> None:
    # A snapshot already exists for 07-05; the bot was down since.
    await history.save_sales("2026-07-05", [_sales("h1", 4)])
    api = _FakeApi()

    await _svc(api, history, "2026-07-10").capture(max_backfill_days=30)

    # Backfills 07-06..07-09 (up to yesterday), and does NOT re-fetch 07-05.
    assert {opts.from_ for opts in api.profit_calls} == {
        "2026-07-06",
        "2026-07-07",
        "2026-07-08",
        "2026-07-09",
    }


async def test_capture_caps_the_backfill_window(history: HistoryStore) -> None:
    api = _FakeApi()
    await _svc(api, history, "2026-07-10").capture(max_backfill_days=2)

    # Only the last 2 complete days, never more than the cap.
    assert {opts.from_ for opts in api.profit_calls} == {"2026-07-08", "2026-07-09"}


async def test_capture_is_idempotent_same_day(history: HistoryStore) -> None:
    api = _FakeApi()
    svc = _svc(api, history, "2026-07-10")

    await svc.capture(max_backfill_days=5)
    calls_after_first = len(api.profit_calls)
    await svc.capture(max_backfill_days=5)  # same day again

    assert api.stock_calls == 1  # today's stock already present ⇒ not re-fetched
    assert len(api.profit_calls) == calls_after_first  # yesterday already stored ⇒ no new sales


async def test_history_tools_registered_and_callable(history: HistoryStore) -> None:
    from typing import Any, cast

    from chic.mcpserver import build_server

    def sales(date: str, qty: float) -> dict[str, object]:
        return {
            "product_href": "h1",
            "name": "Widget",
            "code": "W1",
            "sell_quantity": qty,
            "sell_sum_minor": qty * 1000,
            "sell_cost_minor": qty * 600,
            "profit_minor": qty * 400,
            "return_quantity": 0.0,
            "return_sum_minor": 0.0,
        }

    for i, day in enumerate(("2026-07-01", "2026-07-02", "2026-07-03", "2026-07-04")):
        await history.save_sales(day, [sales(day, 4 + i % 2)])

    server = build_server(cast(Source, _FakeApi()), history)
    names = {t.name for t in await server.list_tools()}
    assert {"xyz_analysis", "abc_xyz_matrix", "purchase_plan"} <= names
    assert len(names) == 26  # 23 base + 3 history-powered

    result = cast("tuple[Any, dict[str, Any]]", await server.call_tool("xyz_analysis", {}))
    out = result[1]
    assert out["totals"]["count"] >= 1
    assert out["rows"][0]["name"] == "Widget"
    # Coverage fields are always present so a caller can gauge data-health.
    assert {"historyDays", "historyGapDays", "historyGaps"} <= out["totals"].keys()


async def test_history_tools_absent_without_history() -> None:
    from typing import cast as _cast

    from chic.mcpserver import build_server

    server = build_server(_cast(Source, _FakeApi()))
    names = {t.name for t in await server.list_tools()}
    assert "xyz_analysis" not in names  # no history store ⇒ not registered
    assert len(names) == 23


