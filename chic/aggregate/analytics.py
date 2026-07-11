"""Analytics: ABC (Pareto), RFM-style segmentation, dead stock, period
comparison, receivables aging. Faithful port of the Go logic (same rounding,
same tie/sort behavior).
"""

from __future__ import annotations

import dataclasses
import math
from datetime import datetime

from chic.aggregate.models import (
    ABCItem,
    ABCTotals,
    Aging,
    AgingBucket,
    AgingItem,
    Change,
    Comparison,
    CounterpartySegment,
    DeadStockLine,
    DeadStockTotals,
    Report,
    SegmentTotals,
)
from chic.aggregate.money import (
    days_between,
    minor_to_major,
    parse_time,
    pct_change,
    round2,
)
from chic.aggregate.report import _truncate, counterparty_metrics, stock
from chic.moysklad.models import CounterpartyRow, Document, StockRow

# ---- ABC analysis ---------------------------------------------------------


def abc(pairs: list[tuple[str, float]], a_cut: float = 0.8, b_cut: float = 0.95) -> list[ABCItem]:
    """Rank (name, value) pairs by value desc and assign A/B/C by cumulative share."""
    if a_cut <= 0 or a_cut >= 1:
        a_cut = 0.8
    if b_cut <= a_cut or b_cut >= 1:
        b_cut = 0.95

    total = sum(v for _, v in pairs if v > 0)
    items = [ABCItem(name=n, value=v) for n, v in pairs]
    items.sort(key=lambda it: it.value, reverse=True)  # stable, ties keep order

    cum = 0.0
    for it in items:
        v = it.value
        if total > 0 and v > 0:
            it.share = round2(v / total * 100)
            cum += v
            it.cumulative_share = round2(cum / total * 100)
        else:
            it.cumulative_share = 100.0
        denom = max(total, 1.0)
        if v <= 0:
            it.abc_class = "C"
        elif cum / denom <= a_cut:
            it.abc_class = "A"
        elif cum / denom <= b_cut:
            it.abc_class = "B"
        else:
            it.abc_class = "C"
    return items


def abc_report(items: list[ABCItem], limit: int) -> Report[ABCItem, ABCTotals]:
    a = b = c = 0
    value = 0.0
    for it in items:
        if it.value > 0:
            value += it.value
        if it.abc_class == "A":
            a += 1
        elif it.abc_class == "B":
            b += 1
        else:
            c += 1
    totals = ABCTotals(count=len(items), value=round2(value), a_count=a, b_count=b, c_count=c)
    shown, full, truncated = _truncate(items, limit)
    return Report[ABCItem, ABCTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- counterparty segmentation (RFM-style rules) --------------------------


@dataclasses.dataclass(frozen=True)
class SegmentParams:
    now: datetime
    sleeping_days: int = 90  # no purchase longer than this ⇒ "sleeping"
    at_risk_days: int = 45  # gap between this and sleeping ⇒ "at_risk"
    vip_top_percent: float = 0.2  # top X by revenue share ⇒ "vip"
    low_check_threshold: float = 0.0  # avg receipt below this ⇒ "low_check" (0 = off)


def _seg_defaults(p: SegmentParams) -> tuple[int, int, float]:
    # `or` gives the Go "0 means default" behavior for these three.
    return (p.sleeping_days or 90, p.at_risk_days or 45, p.vip_top_percent or 0.2)


def segment_counterparties(
    rows: list[CounterpartyRow], params: SegmentParams
) -> list[CounterpartySegment]:
    sleeping_days, at_risk_days, vip_pct = _seg_defaults(params)
    metrics = counterparty_metrics(rows)

    revenues = sorted((m.revenue for m in metrics), reverse=True)
    n = len(revenues)
    vip_threshold = math.inf
    if n > 0:
        idx = int(n * vip_pct)
        if idx >= n:
            idx = n - 1
        vip_threshold = revenues[idx]

    out: list[CounterpartySegment] = []
    for m in metrics:
        segments: list[str] = []
        last = parse_time(m.last_demand)
        days = -1 if last is None else days_between(params.now, last)

        if m.revenue > 0 and m.revenue >= vip_threshold:
            segments.append("vip")
        if days < 0:
            pass  # never purchased; no recency label
        elif days >= sleeping_days:
            segments.append("sleeping")
        elif days >= at_risk_days:
            segments.append("at_risk")
        if (
            params.low_check_threshold > 0
            and m.demands_count > 0
            and (m.avg_receipt < params.low_check_threshold)
        ):
            segments.append("low_check")
        if m.balance > 0:
            segments.append("debtor")
        if m.revenue > 0 and m.profit < 0:
            segments.append("negative_margin")

        out.append(
            CounterpartySegment(
                name=m.name,
                segments=segments,
                revenue=m.revenue,
                avg_receipt=m.avg_receipt,
                profit=m.profit,
                balance=m.balance,
                days_since_last_purchase=days,
            )
        )
    return out


def segment_report(
    segs: list[CounterpartySegment], limit: int
) -> Report[CounterpartySegment, SegmentTotals]:
    tallies = {
        "vip": 0,
        "sleeping": 0,
        "at_risk": 0,
        "low_check": 0,
        "debtor": 0,
        "negative_margin": 0,
    }
    for s in segs:
        for label in s.segments:
            if label in tallies:
                tallies[label] += 1
    totals = SegmentTotals(
        count=len(segs),
        vip=tallies["vip"],
        sleeping=tallies["sleeping"],
        at_risk=tallies["at_risk"],
        low_check=tallies["low_check"],
        debtor=tallies["debtor"],
        negative_margin=tallies["negative_margin"],
    )
    ordered = sorted(segs, key=lambda s: s.revenue, reverse=True)
    shown, full, truncated = _truncate(ordered, limit)
    return Report[CounterpartySegment, SegmentTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- dead stock -----------------------------------------------------------


def dead_stock(
    rows: list[StockRow], outcome_by_ref: dict[str, float] | None, threshold_days: int
) -> list[DeadStockLine]:
    out: list[DeadStockLine] = []
    for r in rows:
        if r.stock <= 0 or r.stock_days < threshold_days:
            continue
        outcome = 0.0
        has_outcome = False
        if outcome_by_ref is not None and r.meta.href in outcome_by_ref:
            outcome = outcome_by_ref[r.meta.href]
            has_outcome = True
        if has_outcome and outcome > 0:
            continue  # it did move ⇒ not dead
        line = stock([r])[0]
        out.append(DeadStockLine(**line.model_dump(), outcome_qty=outcome))
    out.sort(key=lambda x: x.stock_value, reverse=True)
    return out


def dead_stock_report(
    lines: list[DeadStockLine], limit: int
) -> Report[DeadStockLine, DeadStockTotals]:
    totals = DeadStockTotals(
        count=len(lines), stock_value=round2(sum(x.stock_value for x in lines))
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[DeadStockLine, DeadStockTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- period comparison ----------------------------------------------------


def _fold(pairs: list[tuple[str, float]]) -> dict[str, float]:
    m: dict[str, float] = {}
    for k, v in pairs:
        m[k] = m.get(k, 0.0) + v
    return m


def _mk_change(k: str, a: float, b: float) -> Change:
    return Change(
        key=k, value_a=round2(a), value_b=round2(b), delta=round2(b - a), delta_pct=pct_change(a, b)
    )


def compare_periods(
    a: list[tuple[str, float]], b: list[tuple[str, float]], top_n: int
) -> Comparison:
    ma = _fold(a)
    mb = _fold(b)

    seen: set[str] = set()
    changes: list[Change] = []
    total_a = 0.0
    total_b = 0.0
    for k, va in ma.items():
        seen.add(k)
        total_a += va
        changes.append(_mk_change(k, va, mb.get(k, 0.0)))
    for k, vb in mb.items():
        total_b += vb
        if k in seen:
            continue
        changes.append(_mk_change(k, 0.0, vb))

    gainers = sorted(changes, key=lambda c: c.delta, reverse=True)
    decliners = sorted(changes, key=lambda c: c.delta)
    return Comparison(
        total_a=round2(total_a),
        total_b=round2(total_b),
        delta=round2(total_b - total_a),
        delta_pct=pct_change(total_a, total_b),
        top_gainers=[c for c in gainers if c.delta > 0][:top_n],
        top_decliners=[c for c in decliners if c.delta < 0][:top_n],
    )


# ---- receivables aging ----------------------------------------------------

_BUCKET_LABELS = ("current", "1-30", "31-60", "61-90", "90+")


def _bucket_index(days_overdue: int) -> int:
    if days_overdue <= 0:
        return 0
    if days_overdue <= 30:
        return 1
    if days_overdue <= 60:
        return 2
    if days_overdue <= 90:
        return 3
    return 4


def receivables_aging(docs: list[Document], now: datetime, limit: int) -> Aging:
    buckets = [AgingBucket(label=label) for label in _BUCKET_LABELS]
    items: list[AgingItem] = []
    total_outstanding = 0.0
    total_overdue = 0.0

    for d in docs:
        outstanding = minor_to_major(d.sum - d.payed_sum)
        if outstanding <= 0:
            continue
        total_outstanding += outstanding
        name = d.agent.name if d.agent is not None else ""

        due = parse_time(d.payment_planned_moment)
        overdue_days = 0
        if due is not None and due < now:
            overdue_days = days_between(now, due)

        bi = _bucket_index(overdue_days)
        buckets[bi].count += 1
        buckets[bi].amount = round2(buckets[bi].amount + outstanding)
        if overdue_days > 0:
            total_overdue += outstanding

        items.append(
            AgingItem(
                document=d.name,
                counterparty=name,
                due_date=d.payment_planned_moment,
                outstanding=outstanding,
                days_overdue=overdue_days,
            )
        )

    items.sort(key=lambda x: x.days_overdue, reverse=True)
    item_count = len(items)
    truncated = False
    if 0 < limit < len(items):
        items = items[:limit]
        truncated = True

    return Aging(
        total_outstanding=round2(total_outstanding),
        total_overdue=round2(total_overdue),
        buckets=buckets,
        item_count=item_count,
        items_truncated=truncated,
        items=items,
    )
