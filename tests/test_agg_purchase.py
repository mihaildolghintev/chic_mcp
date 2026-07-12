from __future__ import annotations

from decimal import Decimal

from chic.aggregate.models import StockLine
from chic.aggregate.purchase import DemandStat, purchase_plan


def stock(
    name: str, on_hand: float, reserve: float, cost: str, in_transit: float = 0.0
) -> StockLine:
    return StockLine(
        id=name,  # tests use the name as the stable id
        name=name,
        stock=on_hand,
        reserve=reserve,
        available=on_hand - reserve,
        in_transit=in_transit,
        cost_price=Decimal(cost),
        sale_price=Decimal(cost) * 2,
        stock_value=Decimal(cost) * Decimal(on_hand),
        stock_days=1,
    )


def test_purchase_plan_orders_when_below_reorder_point() -> None:
    lines = [stock("Widget", on_hand=10, reserve=2, cost="50")]
    demand = {"Widget": DemandStat(daily_mean=2.0, daily_std=1.0, source="history")}

    report = purchase_plan(
        lines, demand, lead_time_days=7, service_level=0.95, review_period_days=7
    )
    item = report.rows[0]

    # z(0.95)=1.6449, SS=1.6449·1·√7≈4.35, ROP=2·7+4.35≈18.35, S=2·14+4.35≈32.35.
    assert item.source == "history"
    assert item.safety_stock == 4.35
    assert item.reorder_point == 18.35
    assert item.days_of_cover == 4.0  # 8 available / 2 per day
    assert item.to_order == 25  # ceil(32.35 − 8)
    assert item.order_value == Decimal("1250.00")  # 25 × 50
    assert report.totals.need_order == 1


def test_purchase_plan_skips_well_stocked_and_no_demand() -> None:
    lines = [
        stock("Slow", on_hand=100, reserve=0, cost="10"),  # plenty of cover
        stock("Dead", on_hand=5, reserve=0, cost="3"),  # no demand at all
    ]
    demand = {"Slow": DemandStat(daily_mean=1.0, daily_std=0.5, source="estimate")}

    report = purchase_plan(lines, demand, lead_time_days=7, service_level=0.95)
    by_name = {r.name: r for r in report.rows}

    assert by_name["Slow"].to_order == 0  # 100 units ≫ reorder point
    assert by_name["Slow"].days_of_cover == 100.0
    assert by_name["Dead"].source == "none"
    assert by_name["Dead"].to_order == 0
    assert by_name["Dead"].days_of_cover == -1.0  # no demand ⇒ undefined cover
    assert report.totals.need_order == 0
    assert report.totals.order_value == Decimal("0.00")
