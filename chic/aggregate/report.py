"""Map raw MoySklad report rows into compact DTOs (minor→major units here).

Margins are recomputed as ``profit/revenue*100`` so the value is unambiguous
regardless of how MoySklad encodes its own ``margin`` field. Every ``*_report``
returns the uniform truncating envelope: totals over the full set, rows capped.
"""

from __future__ import annotations

from chic.aggregate.models import (
    CounterpartyMetric,
    CounterpartyTotals,
    DashboardSummary,
    MoneyFlow,
    MoneyFlowPoint,
    ProductSummary,
    ProfitEntityLine,
    ProfitProductLine,
    ProfitTotals,
    Report,
    StockLine,
    StockTotals,
    TurnoverLine,
    TurnoverTotals,
)
from chic.aggregate.money import margin_pct, minor_to_major, round2
from chic.moysklad.models import (
    CounterpartyRow,
    Dashboard,
    MoneySeries,
    Product,
    ProfitByEntityRow,
    ProfitByProductRow,
    StockRow,
    TurnoverRow,
)


def _truncate[Row](rows: list[Row], limit: int) -> tuple[list[Row], int, bool]:
    """Cap rows to a display limit (<=0 ⇒ all); report whether truncation happened."""
    full = len(rows)
    if 0 < limit < full:
        return rows[:limit], full, True
    return rows, full, False


# ---- dashboard ------------------------------------------------------------


def dashboard(period: str, d: Dashboard) -> DashboardSummary:
    return DashboardSummary(
        period=period,
        sales_count=d.sales.count,
        sales_amount=minor_to_major(d.sales.amount),
        sales_delta_vs_prev=minor_to_major(d.sales.movement_amount),
        orders_count=d.orders.count,
        orders_amount=minor_to_major(d.orders.amount),
        money_income=minor_to_major(d.money.income),
        money_outcome=minor_to_major(d.money.outcome),
        money_balance=minor_to_major(d.money.balance),
    )


# ---- product --------------------------------------------------------------


def product(p: Product) -> ProductSummary:
    return ProductSummary(
        id=p.id,
        name=p.name,
        code=p.code,
        article=p.article,
        archived=p.archived,
        sale_price=minor_to_major(p.sale_prices[0].value) if p.sale_prices else 0.0,
        buy_price=minor_to_major(p.buy_price.value) if p.buy_price is not None else 0.0,
    )


def products(ps: list[Product]) -> list[ProductSummary]:
    return [product(p) for p in ps]


# ---- profit ---------------------------------------------------------------


def profit_by_product(rows: list[ProfitByProductRow]) -> list[ProfitProductLine]:
    out: list[ProfitProductLine] = []
    for r in rows:
        revenue = minor_to_major(r.sell_sum)
        profit = minor_to_major(r.profit)
        out.append(
            ProfitProductLine(
                name=r.assortment.name,
                code=r.assortment.code,
                sell_quantity=r.sell_quantity,
                revenue=revenue,
                cost=minor_to_major(r.sell_cost_sum),
                return_sum=minor_to_major(r.return_sum),
                profit=profit,
                margin_pct=margin_pct(profit, revenue),
            )
        )
    return out


def profit_by_entity(rows: list[ProfitByEntityRow]) -> list[ProfitEntityLine]:
    out: list[ProfitEntityLine] = []
    for r in rows:
        revenue = minor_to_major(r.sell_sum)
        profit = minor_to_major(r.profit)
        out.append(
            ProfitEntityLine(
                name=r.holder_name,
                revenue=revenue,
                cost=minor_to_major(r.sell_cost_sum),
                profit=profit,
                sales_count=r.sales_count,
                avg_check=minor_to_major(r.sales_avg_check),
                margin_pct=margin_pct(profit, revenue),
            )
        )
    return out


def profit_product_report(
    rows: list[ProfitByProductRow], limit: int
) -> Report[ProfitProductLine, ProfitTotals]:
    lines = sorted(profit_by_product(rows), key=lambda x: x.revenue, reverse=True)
    revenue = sum(x.revenue for x in lines)
    profit = sum(x.profit for x in lines)
    totals = ProfitTotals(
        revenue=round2(revenue),
        cost=round2(sum(x.cost for x in lines)),
        profit=round2(profit),
        return_sum=round2(sum(x.return_sum for x in lines)),
        sell_quantity=round2(sum(x.sell_quantity for x in lines)),
        margin_pct=margin_pct(round2(profit), round2(revenue)),
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[ProfitProductLine, ProfitTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


def profit_entity_report(
    rows: list[ProfitByEntityRow], limit: int
) -> Report[ProfitEntityLine, ProfitTotals]:
    lines = sorted(profit_by_entity(rows), key=lambda x: x.revenue, reverse=True)
    revenue = sum(x.revenue for x in lines)
    profit = sum(x.profit for x in lines)
    totals = ProfitTotals(
        revenue=round2(revenue),
        cost=round2(sum(x.cost for x in lines)),
        profit=round2(profit),
        sales_count=sum(x.sales_count for x in lines),
        margin_pct=margin_pct(round2(profit), round2(revenue)),
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[ProfitEntityLine, ProfitTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- turnover -------------------------------------------------------------


def _turnover_days(r: TurnoverRow, period_days: float) -> float:
    out = r.outcome.quantity
    if out <= 0 or period_days <= 0:
        return 0.0
    avg_stock = (r.on_period_start.quantity + r.on_period_end.quantity) / 2
    daily_out = out / period_days
    if daily_out <= 0:
        return 0.0
    return round2(avg_stock / daily_out)


def turnover(rows: list[TurnoverRow], period_days: float) -> list[TurnoverLine]:
    return [
        TurnoverLine(
            name=r.assortment.name,
            start_qty=r.on_period_start.quantity,
            income_qty=r.income.quantity,
            outcome_qty=r.outcome.quantity,
            end_qty=r.on_period_end.quantity,
            end_value=minor_to_major(r.on_period_end.sum),
            turnover_days=_turnover_days(r, period_days),
        )
        for r in rows
    ]


def turnover_report(
    rows: list[TurnoverRow], period_days: float, limit: int
) -> Report[TurnoverLine, TurnoverTotals]:
    lines = sorted(turnover(rows, period_days), key=lambda x: x.end_value, reverse=True)
    totals = TurnoverTotals(
        income_qty=round2(sum(x.income_qty for x in lines)),
        outcome_qty=round2(sum(x.outcome_qty for x in lines)),
        end_value=round2(sum(x.end_value for x in lines)),
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[TurnoverLine, TurnoverTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- stock ----------------------------------------------------------------


def stock(rows: list[StockRow]) -> list[StockLine]:
    out: list[StockLine] = []
    for r in rows:
        cost = minor_to_major(r.price)
        out.append(
            StockLine(
                name=r.name,
                code=r.code,
                article=r.article,
                stock=r.stock,
                reserve=r.reserve,
                available=r.stock - r.reserve,
                in_transit=r.in_transit,
                cost_price=cost,
                sale_price=minor_to_major(r.sale_price),
                stock_value=round2(r.stock * cost),
                stock_days=int(r.stock_days),
            )
        )
    return out


def stock_report(rows: list[StockRow], limit: int) -> Report[StockLine, StockTotals]:
    lines = sorted(stock(rows), key=lambda x: x.stock_value, reverse=True)
    totals = StockTotals(
        units=round2(sum(x.stock for x in lines)),
        available=round2(sum(x.available for x in lines)),
        stock_value=round2(sum(x.stock_value for x in lines)),
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[StockLine, StockTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- counterparty report --------------------------------------------------


def counterparty_metrics(rows: list[CounterpartyRow]) -> list[CounterpartyMetric]:
    return [
        CounterpartyMetric(
            name=r.counterparty.name,
            first_demand=r.first_demand_date,
            last_demand=r.last_demand_date,
            demands_count=r.demands_count,
            revenue=minor_to_major(r.demands_sum),
            avg_receipt=minor_to_major(r.average_receipt),
            returns_sum=minor_to_major(r.returns_sum),
            balance=minor_to_major(r.balance),
            profit=minor_to_major(r.profit),
        )
        for r in rows
    ]


def counterparty_report(
    rows: list[CounterpartyRow], limit: int
) -> Report[CounterpartyMetric, CounterpartyTotals]:
    lines = sorted(counterparty_metrics(rows), key=lambda x: x.revenue, reverse=True)
    revenue = sum(x.revenue for x in lines)
    profit = sum(x.profit for x in lines)
    totals = CounterpartyTotals(
        revenue=round2(revenue),
        profit=round2(profit),
        returns_sum=round2(sum(x.returns_sum for x in lines)),
        balance=round2(sum(x.balance for x in lines)),
        margin_pct=margin_pct(round2(profit), round2(revenue)),
    )
    shown, full, truncated = _truncate(lines, limit)
    return Report[CounterpartyMetric, CounterpartyTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


# ---- money flow -----------------------------------------------------------


def money(m: MoneySeries) -> MoneyFlow:
    income = minor_to_major(m.credit)
    outcome = minor_to_major(m.debit)
    return MoneyFlow(
        income=income,
        outcome=outcome,
        net=round2(income - outcome),
        series=[
            MoneyFlowPoint(
                date=p.date,
                income=minor_to_major(p.credit),
                outcome=minor_to_major(p.debit),
                balance=minor_to_major(p.balance),
            )
            for p in m.series
        ],
    )
