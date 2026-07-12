"""Application use-cases: fetch-and-aggregate orchestration in one place.

The MCP tools stay thin — each just wraps a use-case for the LLM (``as_object``).
Keeping the "call the API, run the aggregate" step here, returning typed DTOs,
gives it one home ready to be reused by any consumer without duplicating the
orchestration.

Pure by dependency injection: the clock (``now``) is passed in, never read here,
so use-cases stay deterministic and testable.
"""

from __future__ import annotations

from datetime import datetime, timedelta
from decimal import Decimal

from chic import aggregate
from chic.aggregate.bridge import profit_bridge as _bridge
from chic.aggregate.models import ABCXYZMatrix as _ABCXYZMatrix
from chic.aggregate.models import (
    Aging,
    DashboardSummary,
    ProfitBridge,
    ProfitProductLine,
    PurchasePlanItem,
    PurchasePlanTotals,
    Report,
    XYZItem,
    XYZTotals,
)
from chic.aggregate.purchase import DemandStat
from chic.aggregate.purchase import purchase_plan as _plan
from chic.aggregate.report import profit_by_product as _profit_lines
from chic.aggregate.report import stock as _stock_lines
from chic.cache import Source
from chic.history import HistoryStore
from chic.history.analytics import DEFAULT_MIN_DAYS
from chic.history.analytics import abc_xyz_matrix as _abc_xyz
from chic.history.analytics import xyz_analysis as _xyz
from chic.history.olap import DemandStats
from chic.history.olap import demand_stats as _demand_stats
from chic.moysklad import DocumentQuery, DocumentType, ProfitOptions, StockOptions

_FALLBACK_WINDOW_DAYS = 30


# ---- reports --------------------------------------------------------------


async def dashboard(api: Source, period: str) -> DashboardSummary:
    return aggregate.dashboard(period, await api.get_dashboard(period))


async def receivables(api: Source, now: datetime, limit: int = 200) -> Aging:
    docs = await api.search_documents(
        DocumentType.INVOICE_OUT, DocumentQuery(expand=["agent"], order="moment,desc")
    )
    return aggregate.receivables_aging(docs, now, limit)


async def profit_product_lines(
    api: Source, date_from: str, date_to: str
) -> list[ProfitProductLine]:
    rows = await api.profit_by_product(False, ProfitOptions(from_=date_from, to=date_to))
    return _profit_lines(rows)


async def profit_bridge(
    api: Source, a_from: str, a_to: str, b_from: str, b_to: str, top_n: int
) -> ProfitBridge:
    a = await api.profit_by_product(False, ProfitOptions(from_=a_from, to=a_to))
    b = await api.profit_by_product(False, ProfitOptions(from_=b_from, to=b_to))
    return _bridge(_profit_lines(a), _profit_lines(b), top_n)


# ---- history-powered ------------------------------------------------------


async def demand_stats(history: HistoryStore, since: str | None = None) -> list[DemandStats]:
    return _demand_stats(await history.daily_demand(since))


async def xyz(
    history: HistoryStore,
    *,
    since: str | None = None,
    min_days: int,
    x_threshold: float,
    y_threshold: float,
    limit: int,
) -> Report[XYZItem, XYZTotals]:
    return _xyz(
        await demand_stats(history, since),
        min_days=min_days,
        x_threshold=x_threshold,
        y_threshold=y_threshold,
        limit=limit,
        coverage=await history.sales_coverage(),
    )


async def abc_xyz(
    api: Source,
    history: HistoryStore,
    *,
    metric: str,
    date_from: str,
    date_to: str,
    min_days: int,
    limit: int,
) -> _ABCXYZMatrix:
    lines = await profit_product_lines(api, date_from, date_to)
    pairs: list[tuple[str, str, Decimal]] = [
        (x.id, x.name, x.profit if metric == "profit" else x.revenue) for x in lines
    ]
    return _abc_xyz(pairs, await demand_stats(history), min_days=min_days, limit=limit)


async def purchase(
    api: Source,
    history: HistoryStore,
    now: datetime,
    *,
    lead_time_days: float,
    service_level: float,
    review_period_days: float,
    fallback_cv: float,
    limit: int,
) -> Report[PurchasePlanItem, PurchasePlanTotals]:
    stock_rows = await api.get_stock(StockOptions(stock_mode="positiveOnly", group_by="product"))
    stock_lines = _stock_lines(stock_rows)

    # Demand keyed by product id (href): history where a product has enough of it,
    # else a recent-sales estimate.
    stats: dict[str, DemandStat] = {
        s.product_href: DemandStat(s.mean_demand, s.std_demand, "history")
        for s in await demand_stats(history)
        if s.days >= DEFAULT_MIN_DAYS
    }
    fb_from = (now - timedelta(days=_FALLBACK_WINDOW_DAYS)).strftime("%Y-%m-%d")
    fb_to = now.strftime("%Y-%m-%d")
    fb_rows = await api.profit_by_product(False, ProfitOptions(from_=fb_from, to=fb_to))
    cv = max(fallback_cv, 0.0)
    for line in _profit_lines(fb_rows):
        if line.id in stats or line.sell_quantity <= 0:
            continue
        daily = line.sell_quantity / _FALLBACK_WINDOW_DAYS
        stats[line.id] = DemandStat(daily, daily * cv, "estimate")

    return _plan(
        stock_lines,
        stats,
        lead_time_days=lead_time_days,
        service_level=service_level,
        review_period_days=review_period_days,
        limit=limit,
    )
