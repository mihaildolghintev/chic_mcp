from __future__ import annotations

from typing import Any, cast

import pytest
from chic.mcpserver import build_server
from chic.moysklad import DocumentQuery, ListOptions, ProfitOptions, StockOptions
from chic.moysklad.models import (
    AssortmentRow,
    AuditRow,
    Counterparty,
    CounterpartyRow,
    Currency,
    Dashboard,
    DashboardCount,
    DashboardMoney,
    Document,
    EntityRef,
    MoneySeries,
    NamedRef,
    Product,
    ProfitByEntityRow,
    ProfitByProductRow,
    SalesSeries,
    SalesSeriesPoint,
    State,
    StockByStoreRow,
    StockRow,
    StoreStock,
    TurnoverRow,
)


class FakeAPI:
    """Records calls and returns canned MoySklad models (structurally a Source)."""

    def __init__(self) -> None:
        self.calls: dict[str, Any] = {}

    async def list_products(self, opts: ListOptions) -> list[Product]:
        self.calls["list_products"] = opts
        return [Product(id="p1", name="Widget", sale_prices=[])]

    async def search_counterparties(self, opts: ListOptions) -> list[Counterparty]:
        self.calls["search_counterparties"] = opts
        return [Counterparty(id="c1", name="Acme")]

    async def account_currency(self) -> Currency:
        return Currency(iso_code="MDL", name="лей", full_name="Молдавский лей", code="498")

    async def get_dashboard(self, period: str) -> Dashboard:
        return Dashboard(
            sales=DashboardCount(count=3, amount=500000, movement_amount=10000),
            orders=DashboardCount(count=1, amount=100000),
            money=DashboardMoney(income=1000000, outcome=400000, balance=600000),
        )

    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]:
        self.calls["profit_by_product"] = (variant, opts)
        return [
            ProfitByProductRow(assortment=NamedRef(name="Widget"), sell_sum=100000, profit=40000)
        ]

    async def profit_by_entity(
        self, dimension: str, opts: ProfitOptions
    ) -> list[ProfitByEntityRow]:
        self.calls["profit_by_entity"] = (dimension, opts)
        return [
            ProfitByEntityRow(counterparty=NamedRef(name="Acme"), sell_sum=100000, profit=40000)
        ]

    async def get_turnover(self, opts: ProfitOptions) -> list[TurnoverRow]:
        return []

    async def get_stock(self, opts: StockOptions) -> list[StockRow]:
        self.calls["get_stock"] = opts
        return [StockRow(name="Widget", price=5000, stock=10, stock_days=120)]

    async def get_counterparty_report(
        self, filters: list[str], limit: int
    ) -> list[CounterpartyRow]:
        self.calls["get_counterparty_report"] = (filters, limit)
        return [CounterpartyRow(counterparty=NamedRef(name="Acme"), demands_sum=100000)]

    async def get_money_series(self, date_from: str, date_to: str, interval: str) -> MoneySeries:
        return MoneySeries(credit=1000000, debit=400000, series=[])

    async def search_documents(self, doc_type: str, query: DocumentQuery) -> list[Document]:
        self.calls["search_documents"] = (str(doc_type), query)
        return [Document(id="d1", name="INV-1", sum=100000, payed_sum=0)]

    async def get_document(self, doc_type: str, doc_id: str, expand: list[str]) -> Document:
        self.calls["get_document"] = (str(doc_type), doc_id, expand)
        return Document(id=doc_id, name="INV-1", sum=100000)

    async def list_entity_refs(self, entity: str, opts: ListOptions) -> list[EntityRef]:
        self.calls["list_entity_refs"] = (entity, opts)
        return [EntityRef(id="s1", name="Main warehouse", code="MW")]

    async def search_assortment(self, opts: ListOptions) -> list[AssortmentRow]:
        self.calls["search_assortment"] = opts
        row = AssortmentRow(id="a1", name="Bundle", sale_prices=[])
        row.meta.type = "bundle"
        return [row]

    async def list_states(self, doc_type: str) -> list[State]:
        self.calls["list_states"] = str(doc_type)
        return [State(id="st1", name="Новый", state_type="Regular", entity_type="customerorder")]

    async def get_stock_by_store(self, opts: StockOptions) -> list[StockByStoreRow]:
        self.calls["get_stock_by_store"] = opts
        return [
            StockByStoreRow(
                stock_by_store=[StoreStock(name="Main", stock=10, reserve=3, in_transit=0)]
            )
        ]

    async def get_sales_series(
        self,
        kind: str,
        date_from: str,
        date_to: str,
        interval: str,
        *,
        store_id: str = "",
        organization_id: str = "",
        project_id: str = "",
    ) -> SalesSeries:
        self.calls["get_sales_series"] = (kind, date_from, date_to, interval, store_id)
        return SalesSeries(
            series=[SalesSeriesPoint(date="2026-01-01 00:00:00", quantity=5, sum=100000)]
        )

    async def get_audit(self, filters: list[str], limit: int) -> list[AuditRow]:
        self.calls["get_audit"] = (filters, limit)
        return [
            AuditRow(
                moment="2026-01-02 10:00:00",
                uid="user@acme",
                entity_type="customerorder",
                event_type="update",
                object_count=1,
            )
        ]


async def _call(name: str, args: dict[str, Any]) -> dict[str, Any]:
    server = build_server(FakeAPI())
    # FastMCP.call_tool returns (content_blocks, structured_content).
    result = cast("tuple[Any, dict[str, Any]]", await server.call_tool(name, args))
    return result[1]


async def test_all_tools_registered() -> None:
    server = build_server(FakeAPI())
    tools = await server.list_tools()
    names = {t.name for t in tools}
    assert len(names) == 22
    assert "get_dashboard" in names
    assert "receivables_aging" in names
    assert "get_account_currency" in names
    for name in (
        "list_references",
        "list_document_states",
        "search_assortment",
        "get_stock_by_store",
        "get_sales",
        "get_audit",
    ):
        assert name in names


async def test_get_dashboard_camelcase_output() -> None:
    out = await _call("get_dashboard", {"period": "month"})
    assert out["salesAmount"] == 5000.0
    assert out["salesDeltaVsPrev"] == 100.0
    assert out["moneyBalance"] == 6000.0


async def test_list_products_wrapped_as_items_count() -> None:
    out = await _call("list_products", {"query": "widget"})
    assert out["count"] == 1
    assert out["items"][0]["name"] == "Widget"


async def test_get_profit_routes_product_vs_entity() -> None:
    api = FakeAPI()
    server = build_server(api)
    await server.call_tool("get_profit", {"group_by": "product"})
    assert "profit_by_product" in api.calls
    assert "profit_by_entity" not in api.calls

    api2 = FakeAPI()
    server2 = build_server(api2)
    await server2.call_tool("get_profit", {"group_by": "counterparty"})
    assert api2.calls["profit_by_entity"][0] == "counterparty"


async def test_only_debtors_adds_balance_filter() -> None:
    api = FakeAPI()
    server = build_server(api)
    await server.call_tool("get_counterparty_metrics", {"only_debtors": True})
    filters, _ = api.calls["get_counterparty_report"]
    assert filters == ["balance>0"]


async def test_search_documents_currency_expand_by_type() -> None:
    api = FakeAPI()
    server = build_server(api)
    await server.call_tool("search_documents", {"type": "invoiceout"})
    _, query = api.calls["search_documents"]
    assert query.expand == ["rate.currency"]

    api2 = FakeAPI()
    server2 = build_server(api2)
    await server2.call_tool("search_documents", {"type": "move"})
    _, query2 = api2.calls["search_documents"]
    assert query2.expand == []


async def test_get_account_currency_output() -> None:
    out = await _call("get_account_currency", {})
    assert out["isoCode"] == "MDL"
    assert out["name"] == "лей"


async def test_search_counterparty_wrapped() -> None:
    out = await _call("search_counterparty", {"query": "acme"})
    assert out["count"] == 1
    assert out["items"][0]["name"] == "Acme"


async def test_list_references_passes_kind_and_archived_filter() -> None:
    api = FakeAPI()
    server = build_server(api)
    out = cast(
        "tuple[Any, dict[str, Any]]",
        await server.call_tool("list_references", {"kind": "store"}),
    )[1]
    entity, opts = api.calls["list_entity_refs"]
    assert entity == "store"
    assert "archived=false" in opts.filter
    assert out["items"][0]["name"] == "Main warehouse"


async def test_list_references_skips_archived_filter_for_non_archivable() -> None:
    # country/uom/group have no `archived` attribute — the filter would 400.
    for kind in ("country", "uom", "group"):
        api = FakeAPI()
        server = build_server(api)
        await server.call_tool("list_references", {"kind": kind})
        _, opts = api.calls["list_entity_refs"]
        assert opts.filter == []


async def test_search_documents_scoping_filters_reach_query() -> None:
    api = FakeAPI()
    server = build_server(api)
    await server.call_tool(
        "search_documents",
        {"type": "retaildemand", "store_id": "s1", "organization_id": "o1", "state_id": "st1"},
    )
    _, query = api.calls["search_documents"]
    assert query.store_id == "s1"
    assert query.organization_id == "o1"
    assert query.state_id == "st1"


async def test_list_document_states_output() -> None:
    out = await _call("list_document_states", {"document_type": "customerorder"})
    assert out["items"][0]["name"] == "Новый"
    assert out["items"][0]["type"] == "Regular"


async def test_search_assortment_reports_kind() -> None:
    out = await _call("search_assortment", {"query": "bundle"})
    assert out["items"][0]["kind"] == "bundle"


async def test_stock_by_store_aggregates_per_warehouse() -> None:
    out = await _call("get_stock_by_store", {})
    assert out["totals"]["stores"] == 1
    assert out["rows"][0]["store"] == "Main"
    assert out["rows"][0]["available"] == 7.0


async def test_get_sales_defaults_to_prev_month_and_sums() -> None:
    api = FakeAPI()
    server = build_server(api)
    out = cast(
        "tuple[Any, dict[str, Any]]",
        await server.call_tool("get_sales", {"kind": "orders"}),
    )[1]
    kind, date_from, date_to, _interval, _store = api.calls["get_sales_series"]
    assert kind == "orders"
    assert date_from and date_to  # prev-month default filled in
    assert out["totalSum"] == 1000.0


async def test_get_audit_builds_filters() -> None:
    api = FakeAPI()
    server = build_server(api)
    await server.call_tool("get_audit", {"entity_type": "demand", "event_type": "create"})
    filters, _ = api.calls["get_audit"]
    assert "entityType=demand" in filters
    assert "eventType=create" in filters


async def test_invalid_enum_rejected() -> None:
    server = build_server(FakeAPI())
    # Literal-typed enum: an out-of-range period fails schema validation.
    with pytest.raises(Exception):  # noqa: B017 — any validation error is fine
        await server.call_tool("get_dashboard", {"period": "century"})
