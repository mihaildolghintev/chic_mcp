"""Trimmed pydantic models over the MoySklad JSON API 1.2 wire format.

Monetary fields (``sum``, ``price``, ``sell_sum`` …) are **minor units** of the
account currency (kopecks for RUB), exactly as MoySklad returns them — the
conversion to major units happens only in :mod:`chic.aggregate`. ``extra`` is
ignored so unknown fields never break decoding, mirroring the Go structs that
only declared the fields the aggregation layer reads.
"""

from typing import Any

from pydantic import BaseModel, ConfigDict, Field
from pydantic.alias_generators import to_camel


class MSModel(BaseModel):
    model_config = ConfigDict(
        alias_generator=to_camel,
        populate_by_name=True,
        extra="ignore",
    )


class Meta(MSModel):
    href: str = ""
    metadata_href: str = ""
    type: str = ""
    media_type: str = ""
    # List-level meta only:
    size: int = 0
    limit: int = 0
    offset: int = 0
    next_href: str = ""
    previous_href: str = ""


class ListResponse[T](MSModel):
    """The envelope MoySklad wraps every collection (and report) endpoint in."""

    meta: Meta = Field(default_factory=Meta)
    rows: list[T] = Field(default_factory=list)


class NamedRef(MSModel):
    meta: Meta = Field(default_factory=Meta)
    name: str = ""
    code: str = ""


# ---- entity/product -------------------------------------------------------


class PriceType(MSModel):
    name: str = ""


class SalePrice(MSModel):
    value: float = 0.0  # minor units
    price_type: PriceType = Field(default_factory=PriceType)


class BuyPrice(MSModel):
    value: float = 0.0  # minor units


class Product(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    code: str = ""
    article: str = ""
    description: str = ""
    archived: bool = False
    sale_prices: list[SalePrice] = Field(default_factory=list)
    buy_price: BuyPrice | None = None


# ---- entity/currency ------------------------------------------------------


class Currency(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    full_name: str = ""
    iso_code: str = ""
    code: str = ""  # numeric ISO 4217
    default: bool = False


# ---- entity/counterparty --------------------------------------------------


class Counterparty(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    company_type: str = ""
    inn: str = ""
    email: str = ""
    phone: str = ""
    archived: bool = False
    description: str = ""


# ---- report/dashboard -----------------------------------------------------


class DashboardCount(MSModel):
    count: int = 0
    amount: float = 0.0
    movement_amount: float = 0.0


class DashboardMoney(MSModel):
    income: float = 0.0
    outcome: float = 0.0
    balance: float = 0.0
    today_movement: float = 0.0
    movement: float = 0.0


class Dashboard(MSModel):
    sales: DashboardCount = Field(default_factory=DashboardCount)
    orders: DashboardCount = Field(default_factory=DashboardCount)
    money: DashboardMoney = Field(default_factory=DashboardMoney)


# ---- report/profit --------------------------------------------------------


class ProfitByProductRow(MSModel):
    assortment: NamedRef = Field(default_factory=NamedRef)
    sell_quantity: float = 0.0
    sell_sum: float = 0.0
    sell_cost_sum: float = 0.0
    return_quantity: float = 0.0
    return_sum: float = 0.0
    return_cost_sum: float = 0.0
    profit: float = 0.0
    margin: float = 0.0
    sales_margin: float = 0.0


class ProfitByEntityRow(MSModel):
    counterparty: NamedRef | None = None
    employee: NamedRef | None = None
    sales_channel: NamedRef | None = None
    sell_sum: float = 0.0
    sell_cost_sum: float = 0.0
    return_sum: float = 0.0
    return_cost_sum: float = 0.0
    sales_count: int = 0
    sales_avg_check: float = 0.0
    return_count: int = 0
    profit: float = 0.0
    margin: float = 0.0
    sales_margin: float = 0.0

    @property
    def holder_name(self) -> str:
        """The holder's display name regardless of endpoint."""
        if self.counterparty is not None:
            return self.counterparty.name
        if self.employee is not None:
            return self.employee.name
        if self.sales_channel is not None:
            return self.sales_channel.name
        return ""


# ---- report/turnover ------------------------------------------------------


class TurnoverMeasure(MSModel):
    quantity: float = 0.0
    sum: float = 0.0


class TurnoverRow(MSModel):
    assortment: NamedRef = Field(default_factory=NamedRef)
    on_period_start: TurnoverMeasure = Field(default_factory=TurnoverMeasure)
    income: TurnoverMeasure = Field(default_factory=TurnoverMeasure)
    outcome: TurnoverMeasure = Field(default_factory=TurnoverMeasure)
    on_period_end: TurnoverMeasure = Field(default_factory=TurnoverMeasure)


# ---- report/stock/all -----------------------------------------------------


class StockRow(MSModel):
    meta: Meta = Field(default_factory=Meta)
    name: str = ""
    code: str = ""
    article: str = ""
    price: float = 0.0  # cost price, minor units
    sale_price: float = 0.0  # minor units
    stock: float = 0.0
    reserve: float = 0.0
    in_transit: float = 0.0
    quantity: float = 0.0
    # StockDays: API docs say Int, but the live report returns a fractional
    # value (e.g. 410.44) — must be float. Do NOT "fix" back to int.
    stock_days: float = 0.0


# ---- report/counterparty --------------------------------------------------


class CounterpartyRow(MSModel):
    counterparty: NamedRef = Field(default_factory=NamedRef)
    first_demand_date: str = ""
    last_demand_date: str = ""
    demands_count: int = 0
    demands_sum: float = 0.0
    average_receipt: float = 0.0
    returns_count: int = 0
    returns_sum: float = 0.0
    discounts_sum: float = 0.0
    balance: float = 0.0
    profit: float = 0.0
    last_event_date: str = ""


# ---- report/money/plotseries ----------------------------------------------


class MoneySeriesPoint(MSModel):
    date: str = ""
    credit: float = 0.0
    debit: float = 0.0
    balance: float = 0.0


class MoneySeries(MSModel):
    credit: float = 0.0
    debit: float = 0.0
    series: list[MoneySeriesPoint] = Field(default_factory=list)


# ---- documents ------------------------------------------------------------


class Rate(MSModel):
    value: float = 0.0
    currency: Currency | None = None


class Position(MSModel):
    id: str = ""
    quantity: float = 0.0
    price: float = 0.0  # minor units
    discount: float = 0.0  # percentage
    vat: int = 0
    assortment: NamedRef = Field(default_factory=NamedRef)


class Attribute(MSModel):
    """A custom (доп. поле) attribute on a document or entity.

    ``value`` is polymorphic: a scalar for simple types, or a nested object (with
    its own ``name``) for custom-entity / linked references — the aggregation
    layer flattens it to a display value.
    """

    id: str = ""
    name: str = ""
    type: str = ""
    value: Any = None


class Document(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    moment: str = ""
    applicable: bool = False
    description: str = ""
    sum: float = 0.0
    vat_sum: float = 0.0
    payed_sum: float = 0.0
    shipped_sum: float = 0.0
    invoiced_sum: float = 0.0
    payment_planned_moment: str = ""
    delivery_planned_moment: str = ""
    agent: NamedRef | None = None  # counterparty
    organization: NamedRef | None = None
    store: NamedRef | None = None
    sales_channel: NamedRef | None = None
    state: NamedRef | None = None
    rate: Rate | None = None
    positions: ListResponse[Position] | None = None
    attributes: list[Attribute] = Field(default_factory=list)


# ---- entity dictionaries (generic reference) ------------------------------


class EntityRef(MSModel):
    """A lean row for any reference dictionary (store, organization, sales
    channel, employee, project, expense item, product folder, contract…).

    ``extra='ignore'`` lets one model absorb the varied per-entity fields; only
    the identifiers useful for discovering a UUID to feed other tools are kept.
    """

    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    code: str = ""
    external_code: str = ""
    archived: bool = False
    description: str = ""
    path_name: str = ""  # productfolder: parent path
    inn: str = ""  # organization


# ---- entity/{type}/metadata → states --------------------------------------


class State(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    state_type: str = ""  # Regular | Successful | Unsuccessful
    entity_type: str = ""


class EntityMetadata(MSModel):
    """Only the ``states`` collection of an ``/entity/{type}/metadata`` response."""

    states: list[State] = Field(default_factory=list)


# ---- entity/assortment ----------------------------------------------------


class AssortmentRow(MSModel):
    meta: Meta = Field(default_factory=Meta)
    id: str = ""
    name: str = ""
    code: str = ""
    article: str = ""
    archived: bool = False
    sale_prices: list[SalePrice] = Field(default_factory=list)
    buy_price: BuyPrice | None = None
    quantity: float = 0.0  # stock, when the report populates it


# ---- report/stock/bystore -------------------------------------------------


class StoreStock(MSModel):
    meta: Meta = Field(default_factory=Meta)
    name: str = ""  # warehouse name
    stock: float = 0.0
    reserve: float = 0.0
    in_transit: float = 0.0


class StockByStoreRow(MSModel):
    meta: Meta = Field(default_factory=Meta)
    stock_by_store: list[StoreStock] = Field(default_factory=list)


# ---- report/sales|orders/plotseries ---------------------------------------


class SalesSeriesPoint(MSModel):
    date: str = ""
    quantity: float = 0.0
    sum: float = 0.0  # minor units


class SalesSeries(MSModel):
    series: list[SalesSeriesPoint] = Field(default_factory=list)


# ---- audit ----------------------------------------------------------------


class AuditRow(MSModel):
    id: str = ""
    moment: str = ""
    uid: str = ""  # employee login that made the change
    source: str = ""
    entity_type: str = ""
    event_type: str = ""  # create | update | delete | …
    object_count: int = 0
