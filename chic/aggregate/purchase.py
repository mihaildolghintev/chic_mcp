"""Reorder planning: turn stock + a demand forecast into "what to order now".

A textbook, explainable (s, S) inventory model — no ML, no black box:

    safety stock   SS  = z(service_level) · σ_demand · √lead_time
    reorder point  ROP = μ_demand · lead_time + SS
    order-up-to    S   = μ_demand · (lead_time + review_period) + SS
    order now          = ⌈S − available⌉   (only when available < ROP)

``z`` comes from ``statistics.NormalDist`` (no scipy). Demand statistics come from
the snapshot history where a product has enough of it, and from a coarse recent-sales
estimate otherwise (flagged ``source="estimate"``) so a plan exists from day one.
Money stays Decimal end to end.
"""

from __future__ import annotations

import dataclasses
import math
from statistics import NormalDist

from chic.aggregate.envelope import truncate
from chic.aggregate.models import (
    PurchasePlanItem,
    PurchasePlanTotals,
    Report,
    StockLine,
)
from chic.aggregate.money import dec, money_round, round2


@dataclasses.dataclass(frozen=True)
class DemandStat:
    daily_mean: float
    daily_std: float
    source: str  # history | estimate | none


def _z(service_level: float) -> float:
    level = min(max(service_level, 0.5), 0.999)
    return NormalDist().inv_cdf(level)


def purchase_plan(
    stock: list[StockLine],
    demand: dict[str, DemandStat],
    *,
    lead_time_days: float = 7.0,
    service_level: float = 0.95,
    review_period_days: float = 7.0,
    limit: int = 100,
) -> Report[PurchasePlanItem, PurchasePlanTotals]:
    z = _z(service_level)
    lead = max(lead_time_days, 0.0)
    review = max(review_period_days, 0.0)

    items: list[PurchasePlanItem] = []
    for line in stock:
        # Join on the stable product id (href), not the display name.
        stat = demand.get(line.id, DemandStat(0.0, 0.0, "none"))
        mu, sigma = stat.daily_mean, stat.daily_std
        available = line.available + line.in_transit

        safety = z * sigma * math.sqrt(lead) if mu > 0 else 0.0
        rop = mu * lead + safety
        order_up_to = mu * (lead + review) + safety
        to_order = 0
        if mu > 0 and available < rop:
            to_order = max(0, math.ceil(order_up_to - available))
        days_cover = round2(available / mu) if mu > 0 else -1.0

        items.append(
            PurchasePlanItem(
                name=line.name,
                code=line.code,
                available=round2(available),
                daily_demand=round2(mu),
                demand_std=round2(sigma),
                source=stat.source,
                days_of_cover=days_cover,
                safety_stock=round2(safety),
                reorder_point=round2(rop),
                to_order=to_order,
                order_value=money_round(dec(to_order) * line.cost_price),
            )
        )

    items.sort(key=lambda it: it.to_order, reverse=True)
    order_value = sum((it.order_value for it in items), dec(0))
    totals = PurchasePlanTotals(
        count=len(items),
        need_order=sum(1 for it in items if it.to_order > 0),
        order_value=money_round(order_value),
    )
    shown, full, truncated = truncate(items, limit)
    return Report[PurchasePlanItem, PurchasePlanTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )
