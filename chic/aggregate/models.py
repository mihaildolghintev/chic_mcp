"""Compact, LLM-friendly output models (major units), mirroring the Go DTOs.

Python field names are chosen so their camelCase form matches the Go ``json``
tags exactly (e.g. ``sales_delta_vs_prev`` → ``salesDeltaVsPrev``); the only
explicit alias is ``class`` (a Python keyword). Serialize with
``model_dump(by_alias=True)`` to reproduce the Go wire shape.
"""

from __future__ import annotations

from decimal import Decimal
from typing import Annotated, Any

from pydantic import BaseModel, ConfigDict, Field, PlainSerializer
from pydantic.alias_generators import to_camel

# A money amount. Stored as Decimal (exact, half-away arithmetic) but emitted as a
# JSON number so the wire shape stays numeric for the LLM and the golden fixtures.
Money = Annotated[
    Decimal, PlainSerializer(lambda d: float(d), return_type=float, when_used="always")
]


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
    sales_amount: Money
    sales_delta_vs_prev: Money
    orders_count: int
    orders_amount: Money
    money_income: Money
    money_outcome: Money
    money_balance: Money


# ---- profit ---------------------------------------------------------------


class ProfitProductLine(OutModel):
    id: str = ""  # product href — the stable join key (name can collide/change)
    name: str
    code: str = ""
    sell_quantity: float
    revenue: Money
    cost: Money
    return_sum: Money
    profit: Money
    margin_pct: float


class ProfitEntityLine(OutModel):
    name: str
    revenue: Money
    cost: Money
    profit: Money
    sales_count: int
    avg_check: Money
    margin_pct: float


class ProfitTotals(OutModel):
    revenue: Money
    cost: Money
    profit: Money
    return_sum: Money = Decimal(0)
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
    end_value: Money
    turnover_days: float


class TurnoverTotals(OutModel):
    income_qty: float
    outcome_qty: float
    end_value: Money


# ---- stock ----------------------------------------------------------------


class StockLine(OutModel):
    id: str = ""  # product href — the stable join key (name can collide/change)
    name: str
    code: str = ""
    article: str = ""
    stock: float
    reserve: float
    available: float
    in_transit: float
    cost_price: Money
    sale_price: Money
    stock_value: Money
    stock_days: int


class StockTotals(OutModel):
    units: float
    available: float
    stock_value: Money


# ---- counterparty report --------------------------------------------------


class CounterpartyMetric(OutModel):
    name: str
    first_demand: str = ""
    last_demand: str = ""
    demands_count: int
    revenue: Money
    avg_receipt: Money
    returns_sum: Money
    balance: Money
    profit: Money


class CounterpartyTotals(OutModel):
    revenue: Money
    profit: Money
    returns_sum: Money = Decimal(0)
    balance: Money
    margin_pct: float


# ---- money flow -----------------------------------------------------------


class MoneyFlowPoint(OutModel):
    date: str
    income: Money
    outcome: Money
    balance: Money


class MoneyFlow(OutModel):
    income: Money
    outcome: Money
    net: Money
    series: list[MoneyFlowPoint]


# ---- documents ------------------------------------------------------------


class DocumentSummary(OutModel):
    id: str
    name: str
    moment: str
    sum: Money
    paid: Money = Decimal(0)
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
    price: Money
    discount: float = 0.0
    vat: int = 0
    total: Money


class DocumentDetail(DocumentSummary):
    description: str = ""
    vat_sum: Money = Decimal(0)
    payment_due_date: str = ""
    positions: list[PositionRow] = Field(default_factory=list)
    attributes: dict[str, Any] = Field(default_factory=dict)


class DocumentTotals(OutModel):
    sum: Money
    paid: Money


# ---- reference dictionaries -----------------------------------------------


class EntityRefOut(OutModel):
    id: str
    name: str
    code: str = ""
    archived: bool = False
    description: str = ""
    path_name: str = ""
    inn: str = ""


class StateOut(OutModel):
    id: str
    name: str
    type: str = ""  # Regular | Successful | Unsuccessful
    entity_type: str = ""


# ---- assortment -----------------------------------------------------------


class AssortmentLine(OutModel):
    id: str
    kind: str  # product | variant | bundle | service | consignment
    name: str
    code: str = ""
    article: str = ""
    sale_price: Money = Decimal(0)
    buy_price: Money = Decimal(0)
    stock: float = 0.0


# ---- stock by store -------------------------------------------------------


class StoreStockLine(OutModel):
    store: str
    positions: int
    units: float
    reserve: float
    available: float


class StoreStockTotals(OutModel):
    stores: int
    units: float
    reserve: float
    available: float


# ---- sales / orders series ------------------------------------------------


class SalesSeriesPointOut(OutModel):
    date: str
    quantity: float
    sum: Money


class SalesSeriesOut(OutModel):
    kind: str  # sales | orders
    total_quantity: float
    total_sum: Money
    series: list[SalesSeriesPointOut] = Field(default_factory=list)


# ---- audit ----------------------------------------------------------------


class AuditLine(OutModel):
    moment: str
    employee: str = ""
    entity_type: str = ""
    event_type: str = ""
    object_count: int = 0
    source: str = ""


# ---- product --------------------------------------------------------------


class ProductSummary(OutModel):
    id: str
    name: str
    code: str = ""
    article: str = ""
    archived: bool
    sale_price: Money
    buy_price: Money


# ---- ABC ------------------------------------------------------------------


class ABCItem(OutModel):
    name: str
    value: Money
    share: float = 0.0
    cumulative_share: float = 0.0
    abc_class: str = Field(default="C", alias="class")


class ABCTotals(OutModel):
    count: int
    value: Money
    a_count: int
    b_count: int
    c_count: int


# ---- counterparty segmentation --------------------------------------------


class CounterpartySegment(OutModel):
    name: str
    segments: list[str] = Field(default_factory=list)
    revenue: Money
    avg_receipt: Money
    profit: Money
    balance: Money
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
    stock_value: Money


# ---- period comparison ----------------------------------------------------


class Change(OutModel):
    key: str
    value_a: Money
    value_b: Money
    delta: Money
    delta_pct: float


class Comparison(OutModel):
    total_a: Money
    total_b: Money
    delta: Money
    delta_pct: float
    top_gainers: list[Change]
    top_decliners: list[Change]


# ---- profit bridge (price / volume / mix / cost decomposition) -------------


class BridgeDriver(OutModel):
    """One product's contribution to the gross-profit change between two periods.

    For products present in both periods (``kind == "common"``) the three effects
    sum exactly to ``delta``. For ``new`` / ``discontinued`` products the effects
    are zero and the whole ``delta`` sits in the corresponding aggregate bucket.
    """

    name: str
    kind: str  # common | new | discontinued
    qty_a: float
    qty_b: float
    profit_a: Money
    profit_b: Money
    delta: Money
    price_effect: Money
    cost_effect: Money
    qty_effect: Money  # volume + mix, at product level


class ProfitBridge(OutModel):
    """Gross-profit change decomposed into price, volume, mix and cost effects.

    Gross profit is ``revenue - cost`` per product, so the six effects plus the
    ``rounding`` residual reconcile to ``delta`` to the cent. ``price``/``cost``/
    ``volume``/``mix`` cover products sold in both periods; products that appeared
    or disappeared are attributed to ``new_products`` / ``discontinued``.
    """

    profit_a: Money
    profit_b: Money
    delta: Money
    price_effect: Money
    cost_effect: Money
    volume_effect: Money
    mix_effect: Money
    new_products_effect: Money
    discontinued_effect: Money
    rounding: Money  # residual so the rounded effects sum back to delta exactly
    common_count: int
    new_count: int
    discontinued_count: int
    top_drivers: list[BridgeDriver]


# ---- XYZ (demand variability, over snapshot history) ----------------------


class XYZItem(OutModel):
    name: str
    code: str = ""
    days: int  # observed sales-days used for the statistic
    mean_demand: float  # average daily units
    std_demand: float  # population stdev of daily units
    cv: float  # coefficient of variation σ/μ
    xyz_class: str = "Z"  # X stable | Y variable | Z erratic (wire: xyzClass)


class XYZTotals(OutModel):
    count: int
    x_count: int
    y_count: int
    z_count: int
    unclassified: int  # products with too little history to classify
    # Data-health of the underlying snapshot history, so a caller can tell whether a
    # classification rests on complete data or a series with outage gaps.
    history_days: int = 0  # days the snapshot job actually captured
    history_gap_days: int = 0  # calendar days in the covered span never captured
    history_gaps: list[str] = Field(default_factory=list)  # first few missing dates


# ---- ABC/XYZ matrix -------------------------------------------------------


class ABCXYZCell(OutModel):
    cell: str  # e.g. "AX"
    count: int
    revenue: Money  # total revenue of the products in this cell (period ABC input)
    recommendation: str


class ABCXYZItem(OutModel):
    name: str
    code: str = ""
    abc_class: str  # wire: abcClass
    xyz_class: str  # wire: xyzClass
    cell: str
    revenue: Money
    cv: float


class ABCXYZMatrix(OutModel):
    classified: int
    abc_only: int  # products with ABC (revenue) but no XYZ history
    cells: list[ABCXYZCell]
    items: list[ABCXYZItem]
    item_count: int
    items_truncated: bool = False


# ---- purchase plan (reorder forecast) -------------------------------------


class PurchasePlanItem(OutModel):
    name: str
    code: str = ""
    available: float  # on hand − reserved + in transit
    daily_demand: float  # forecast mean daily units
    demand_std: float  # daily demand stdev
    source: str  # history | estimate | none — where the demand forecast came from
    days_of_cover: float  # available ÷ daily demand; -1 when there is no demand
    safety_stock: float
    reorder_point: float
    to_order: int  # units to order now (0 unless below the reorder point)
    order_value: Money  # to_order × cost price


class PurchasePlanTotals(OutModel):
    count: int
    need_order: int  # products with to_order > 0
    order_value: Money


# ---- receivables aging ----------------------------------------------------


class AgingItem(OutModel):
    document: str
    counterparty: str = ""
    due_date: str = ""
    outstanding: Money
    days_overdue: int


class AgingBucket(OutModel):
    label: str
    count: int = 0
    amount: Money = Decimal(0)


class Aging(OutModel):
    total_outstanding: Money
    total_overdue: Money
    buckets: list[AgingBucket]
    item_count: int
    items_truncated: bool = False
    items: list[AgingItem]
