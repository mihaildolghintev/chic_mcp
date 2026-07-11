from __future__ import annotations

from datetime import datetime

from chic.aggregate.analytics import (
    SegmentParams,
    compare_periods,
    dead_stock,
    receivables_aging,
    segment_counterparties,
)
from chic.moysklad.models import (
    CounterpartyRow,
    Document,
    Meta,
    NamedRef,
    StockRow,
)

NOW = datetime(2026, 7, 12, 0, 0, 0)


def _cp(
    name: str,
    *,
    demands_sum: float,
    last: str,
    balance: float = 0.0,
    profit: float = 0.0,
    demands_count: int = 1,
    avg: float = 0.0,
) -> CounterpartyRow:
    return CounterpartyRow(
        counterparty=NamedRef(name=name),
        demands_sum=demands_sum,
        last_demand_date=last,
        balance=balance,
        profit=profit,
        demands_count=demands_count,
        average_receipt=avg,
    )


def test_segment_labels() -> None:
    rows = [
        _cp("VIP", demands_sum=10_000_00, last="2026-07-01 00:00:00", profit=500_00),
        _cp("Sleeper", demands_sum=100_00, last="2026-01-01 00:00:00"),  # >90 days
        _cp("Debtor", demands_sum=200_00, last="2026-07-05 00:00:00", balance=50_00),
        _cp("Loss", demands_sum=300_00, last="2026-07-05 00:00:00", profit=-10_00),
    ]
    segs = {s.name: s for s in segment_counterparties(rows, SegmentParams(now=NOW))}
    assert "vip" in segs["VIP"].segments  # top-revenue counterparty
    assert "sleeping" in segs["Sleeper"].segments
    assert "debtor" in segs["Debtor"].segments
    assert "negative_margin" in segs["Loss"].segments


def test_segment_never_purchased_has_no_recency_label() -> None:
    rows = [_cp("New", demands_sum=0.0, last="", demands_count=0)]
    seg = segment_counterparties(rows, SegmentParams(now=NOW))[0]
    assert seg.days_since_last_purchase == -1
    assert "sleeping" not in seg.segments
    assert "at_risk" not in seg.segments


def test_dead_stock_excludes_moved_items() -> None:
    rows = [
        StockRow(meta=Meta(href="ref/frozen"), name="Frozen", stock=5, price=10000, stock_days=200),
        StockRow(meta=Meta(href="ref/moved"), name="Moved", stock=5, price=10000, stock_days=200),
        StockRow(meta=Meta(href="ref/fresh"), name="Fresh", stock=5, price=10000, stock_days=10),
    ]
    outcome = {"ref/moved": 3.0}  # Moved had outbound movement
    lines = dead_stock(rows, outcome, threshold_days=90)
    names = [line.name for line in lines]
    assert names == ["Frozen"]  # Fresh below threshold, Moved excluded by outcome


def test_compare_periods_gainers_and_decliners() -> None:
    a = [("x", 100.0), ("y", 50.0)]
    b = [("x", 120.0), ("z", 30.0)]
    cmp = compare_periods(a, b, top_n=10)
    assert cmp.total_a == 150.0
    assert cmp.total_b == 150.0
    assert cmp.delta == 0.0
    assert [c.key for c in cmp.top_gainers] == ["z", "x"]  # +30, +20
    assert [c.key for c in cmp.top_decliners] == ["y"]  # -50


def test_receivables_aging_buckets() -> None:
    docs = [
        Document(
            name="INV-1",
            sum=100_000,
            payed_sum=0,
            payment_planned_moment="2026-06-01 00:00:00",
            agent=NamedRef(name="Acme"),
        ),
        Document(name="INV-2", sum=50_000, payed_sum=50_000),  # fully paid ⇒ skipped
        Document(name="INV-3", sum=20_000, payed_sum=0),  # no due date ⇒ current
    ]
    aging = receivables_aging(docs, NOW, limit=0)
    assert aging.total_outstanding == 1200.0  # 1000 + 200
    assert aging.total_overdue == 1000.0  # only INV-1 is overdue
    assert aging.item_count == 2
    assert aging.items[0].document == "INV-1"  # sorted by days overdue desc
    labels = {b.label: b for b in aging.buckets}
    assert labels["31-60"].count == 1  # INV-1 is ~41 days overdue
    assert labels["current"].count == 1  # INV-3
