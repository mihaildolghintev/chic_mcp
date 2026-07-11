from __future__ import annotations

from chic.aggregate import (
    dashboard,
    profit_product_report,
    stock_report,
    turnover_report,
)
from chic.moysklad.models import (
    Dashboard,
    DashboardCount,
    DashboardMoney,
    NamedRef,
    ProfitByProductRow,
    StockRow,
    TurnoverMeasure,
    TurnoverRow,
)


def test_dashboard_converts_minor_units() -> None:
    d = Dashboard(
        sales=DashboardCount(count=3, amount=500000, movement_amount=10000),
        orders=DashboardCount(count=2, amount=200000),
        money=DashboardMoney(income=1000000, outcome=400000, balance=600000),
    )
    s = dashboard("month", d)
    assert s.sales_count == 3
    assert s.sales_amount == 5000.0
    assert s.sales_delta_vs_prev == 100.0
    assert s.money_balance == 6000.0


def test_profit_product_report_sorts_totals_and_margin() -> None:
    rows = [
        ProfitByProductRow(
            assortment=NamedRef(name="Cheap"), sell_sum=10000, profit=2000, sell_cost_sum=8000
        ),
        ProfitByProductRow(
            assortment=NamedRef(name="Pricey"), sell_sum=100000, profit=40000, sell_cost_sum=60000
        ),
    ]
    report = profit_product_report(rows, limit=0)
    assert [r.name for r in report.rows] == ["Pricey", "Cheap"]  # by revenue desc
    assert report.totals.revenue == 1100.0  # (100000 + 10000) / 100
    assert report.totals.profit == 420.0
    assert report.totals.margin_pct == round((420.0 / 1100.0) * 100, 2)


def test_stock_report_value_and_available() -> None:
    rows = [
        StockRow(name="Widget", price=5000, stock=10, reserve=2, sale_price=9000, stock_days=120),
    ]
    report = stock_report(rows, limit=0)
    line = report.rows[0]
    assert line.cost_price == 50.0
    assert line.available == 8.0
    assert line.stock_value == 500.0  # 10 * 50.0
    assert line.stock_days == 120
    assert report.totals.stock_value == 500.0


def test_turnover_days() -> None:
    rows = [
        TurnoverRow(
            assortment=NamedRef(name="Item"),
            on_period_start=TurnoverMeasure(quantity=100),
            income=TurnoverMeasure(quantity=0),
            outcome=TurnoverMeasure(quantity=30),
            on_period_end=TurnoverMeasure(quantity=100, sum=500000),
        ),
    ]
    report = turnover_report(rows, period_days=30, limit=0)
    line = report.rows[0]
    # avg_stock=100, daily_out=30/30=1 → turnover_days = 100/1 = 100
    assert line.turnover_days == 100.0
    assert line.end_value == 5000.0


def test_turnover_days_zero_when_no_outbound() -> None:
    rows = [
        TurnoverRow(
            assortment=NamedRef(name="Idle"),
            on_period_start=TurnoverMeasure(quantity=50),
            outcome=TurnoverMeasure(quantity=0),
            on_period_end=TurnoverMeasure(quantity=50),
        ),
    ]
    report = turnover_report(rows, period_days=30, limit=0)
    assert report.rows[0].turnover_days == 0.0
