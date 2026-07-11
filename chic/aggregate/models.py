"""Compact, LLM-friendly output models (major units), mirroring the Go DTOs.

Python field names are chosen so their camelCase form matches the Go ``json``
tags exactly (e.g. ``sales_delta_vs_prev`` → ``salesDeltaVsPrev``); the only
explicit alias is ``class`` (a Python keyword). Serialize with
``model_dump(by_alias=True)`` to reproduce the Go wire shape.
"""

from __future__ import annotations

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


class OutModel(BaseModel):
    model_config = ConfigDict(alias_generator=to_camel, populate_by_name=True)


# ---- uniform truncating envelope ------------------------------------------


class Report[Row, Totals](OutModel):
    """Totals always cover the full result set; rows are truncated to a limit."""

    totals: Totals
    row_count: int
    returned: int
    truncated: bool
    rows: list[Row]


# ---- dashboard ------------------------------------------------------------


class DashboardSummary(OutModel):
    period: str
    sales_count: int
    sales_amount: float
    sales_delta_vs_prev: float
    orders_count: int
    orders_amount: float
    money_income: float
    money_outcome: float
    money_balance: float


# ---- profit ---------------------------------------------------------------


class ProfitProductLine(OutModel):
    name: str
    code: str = ""
    sell_quantity: float
    revenue: float
    cost: float
    return_sum: float
    profit: float
    margin_pct: float


class ProfitEntityLine(OutModel):
    name: str
    revenue: float
    cost: float
    profit: float
    sales_count: int
    avg_check: float
    margin_pct: float


class ProfitTotals(OutModel):
    revenue: float
    cost: float
    profit: float
    return_sum: float = 0.0
    sell_quantity: float = 0.0
    sales_count: int = 0
    margin_pct: float = 0.0


# ---- turnover -------------------------------------------------------------


class TurnoverLine(OutModel):
    name: str
    start_qty: float
    income_qty: float
    outcome_qty: float
    end_qty: float
    end_value: float
    turnover_days: float


class TurnoverTotals(OutModel):
    income_qty: float
    outcome_qty: float
    end_value: float


# ---- stock ----------------------------------------------------------------


class StockLine(OutModel):
    name: str
    code: str = ""
    article: str = ""
    stock: float
    reserve: float
    available: float
    in_transit: float
    cost_price: float
    sale_price: float
    stock_value: float
    stock_days: int


class StockTotals(OutModel):
    units: float
    available: float
    stock_value: float


# ---- counterparty report --------------------------------------------------


class CounterpartyMetric(OutModel):
    name: str
    first_demand: str = ""
    last_demand: str = ""
    demands_count: int
    revenue: float
    avg_receipt: float
    returns_sum: float
    balance: float
    profit: float


class CounterpartyTotals(OutModel):
    revenue: float
    profit: float
    returns_sum: float = 0.0
    balance: float
    margin_pct: float


# ---- money flow -----------------------------------------------------------


class MoneyFlowPoint(OutModel):
    date: str
    income: float
    outcome: float
    balance: float


class MoneyFlow(OutModel):
    income: float
    outcome: float
    net: float
    series: list[MoneyFlowPoint]


# ---- documents ------------------------------------------------------------


class DocumentSummary(OutModel):
    id: str
    name: str
    moment: str
    sum: float
    paid: float = 0.0
    counterparty: str = ""
    state: str = ""
    store: str = ""
    channel: str = ""
    currency: str = ""
    applicable: bool = False


class PositionRow(OutModel):
    name: str
    code: str = ""
    quantity: float
    price: float
    discount: float = 0.0
    vat: int = 0
    total: float


class DocumentDetail(DocumentSummary):
    description: str = ""
    vat_sum: float = 0.0
    payment_due_date: str = ""
    positions: list[PositionRow] = Field(default_factory=list)


class DocumentTotals(OutModel):
    sum: float
    paid: float


# ---- product --------------------------------------------------------------


class ProductSummary(OutModel):
    id: str
    name: str
    code: str = ""
    article: str = ""
    archived: bool
    sale_price: float
    buy_price: float


# ---- ABC ------------------------------------------------------------------


class ABCItem(OutModel):
    name: str
    value: float
    share: float = 0.0
    cumulative_share: float = 0.0
    abc_class: str = Field(default="C", alias="class")


class ABCTotals(OutModel):
    count: int
    value: float
    a_count: int
    b_count: int
    c_count: int


# ---- counterparty segmentation --------------------------------------------


class CounterpartySegment(OutModel):
    name: str
    segments: list[str] = Field(default_factory=list)
    revenue: float
    avg_receipt: float
    profit: float
    balance: float
    days_since_last_purchase: int


class SegmentTotals(OutModel):
    count: int
    vip: int
    sleeping: int
    at_risk: int
    low_check: int
    debtor: int
    negative_margin: int


# ---- dead stock -----------------------------------------------------------


class DeadStockLine(StockLine):
    outcome_qty: float = 0.0


class DeadStockTotals(OutModel):
    count: int
    stock_value: float


# ---- period comparison ----------------------------------------------------


class Change(OutModel):
    key: str
    value_a: float
    value_b: float
    delta: float
    delta_pct: float


class Comparison(OutModel):
    total_a: float
    total_b: float
    delta: float
    delta_pct: float
    top_gainers: list[Change]
    top_decliners: list[Change]


# ---- receivables aging ----------------------------------------------------


class AgingItem(OutModel):
    document: str
    counterparty: str = ""
    due_date: str = ""
    outstanding: float
    days_overdue: int


class AgingBucket(OutModel):
    label: str
    count: int = 0
    amount: float = 0.0


class Aging(OutModel):
    total_outstanding: float
    total_overdue: float
    buckets: list[AgingBucket]
    item_count: int
    items_truncated: bool = False
    items: list[AgingItem]
