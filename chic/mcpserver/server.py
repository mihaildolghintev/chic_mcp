"""MoySklad analytics exposed as MCP tools (FastMCP).

Every tool is read-only. `build_server(api)` registers all 15 tools closing over
the given MoySklad client (raw or cache-wrapped). Reports return the uniform
`{totals, rowCount, returned, truncated, rows}` envelope so the model uses totals
rather than re-summing possibly-truncated rows.
"""

from __future__ import annotations

from typing import Annotated, Any, Literal

from mcp.server.fastmcp import FastMCP
from pydantic import Field

from chic import aggregate
from chic.aggregate.analytics import SegmentParams
from chic.aggregate.report import profit_by_entity, profit_by_product
from chic.cache import Source
from chic.mcpserver.helpers import as_object, clamp_limit, now, period_days, prev_month
from chic.moysklad import (
    DocumentQuery,
    DocumentType,
    ListOptions,
    ProfitOptions,
    StockOptions,
    has_currency,
)
from chic.moysklad.models import ProfitByEntityRow, ProfitByProductRow

DocTypeArg = Literal[
    "demand",
    "customerorder",
    "supply",
    "purchaseorder",
    "invoiceout",
    "invoicein",
    "salesreturn",
    "purchasereturn",
    "paymentin",
    "paymentout",
    "move",
    "inventory",
    "loss",
    "enter",
    "processing",
]


def _product_pairs(rows: list[ProfitByProductRow], metric: str) -> list[tuple[str, float]]:
    return [
        (x.name, x.profit if metric == "profit" else x.revenue) for x in profit_by_product(rows)
    ]


def _entity_pairs(rows: list[ProfitByEntityRow], metric: str) -> list[tuple[str, float]]:
    return [(x.name, x.profit if metric == "profit" else x.revenue) for x in profit_by_entity(rows)]


def build_server(api: Source) -> FastMCP:
    mcp = FastMCP("moysklad-mcp")

    # ---- products ---------------------------------------------------------

    @mcp.tool()
    async def list_products(
        query: Annotated[str, Field(description="Full-text search over name/code/article.")] = "",
        include_archived: Annotated[bool, Field(description="Include archived products.")] = False,
        limit: Annotated[int, Field(description="Max rows. Default 100, max 1000.")] = 100,
    ) -> dict[str, Any]:
        """List products from the MoySklad catalog (id, name, code, article, archived,
        sale price, buy price). Prices are in the account's base currency."""
        opts = ListOptions(search=query, limit=clamp_limit(limit), order="name,asc")
        if not include_archived:
            opts.filter.append("archived=false")
        return as_object(aggregate.products(await api.list_products(opts)))

    # ---- reports ----------------------------------------------------------

    @mcp.tool()
    async def get_dashboard(
        period: Annotated[
            Literal["day", "week", "month"], Field(description="Window. Default month.")
        ] = "month",
    ) -> dict[str, Any]:
        """Quick business summary for a window: sales, orders, money in/out/balance,
        with the change vs the previous comparable period. Base currency."""
        return as_object(aggregate.dashboard(period, await api.get_dashboard(period)))

    @mcp.tool()
    async def get_profit(
        group_by: Annotated[
            Literal["product", "variant", "counterparty", "saleschannel", "employee"],
            Field(description="Grouping dimension. Default product."),
        ] = "product",
        date_from: Annotated[str, Field(description="Period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Period end YYYY-MM-DD.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Profitability over a period grouped by a dimension: revenue, cost, profit,
        margin. `totals` cover every row; use them instead of re-summing `rows`."""
        opts = ProfitOptions(from_=date_from, to=date_to)
        display = clamp_limit(limit)
        if group_by in ("product", "variant"):
            rows = await api.profit_by_product(group_by == "variant", opts)
            return as_object(aggregate.profit_product_report(rows, display))
        entity_rows = await api.profit_by_entity(group_by, opts)
        return as_object(aggregate.profit_entity_report(entity_rows, display))

    @mcp.tool()
    async def get_turnover(
        date_from: Annotated[str, Field(description="Period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Period end YYYY-MM-DD.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """Inventory turnover per product: opening/closing stock, goods in/out, and
        turnover days. No dates ⇒ previous full month. Base currency."""
        if not date_from and not date_to:
            date_from, date_to = prev_month(now())
        rows = await api.get_turnover(ProfitOptions(from_=date_from, to=date_to))
        return as_object(
            aggregate.turnover_report(rows, period_days(date_from, date_to, 30), clamp_limit(limit))
        )

    @mcp.tool()
    async def get_stock(
        stock_mode: Annotated[
            Literal["nonEmpty", "all", "positiveOnly", "negativeOnly", "underMinimum", "empty"],
            Field(description="Stock filter. Default nonEmpty."),
        ] = "nonEmpty",
        date: Annotated[str, Field(description="Stock as of this date YYYY-MM-DD.")] = "",
        store: Annotated[str, Field(description="Warehouse UUID to scope to.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """Warehouse stock: on-hand, reserved, available, in-transit, cost/sale price,
        stock value, age in days. Base currency."""
        rows = await api.get_stock(
            StockOptions(stock_mode=stock_mode, group_by="product", moment=date, store_id=store)
        )
        return as_object(aggregate.stock_report(rows, clamp_limit(limit)))

    @mcp.tool()
    async def get_counterparty_metrics(
        only_debtors: Annotated[
            bool, Field(description="Only counterparties with positive balance.")
        ] = False,
        limit: Annotated[int, Field(description="Max detail rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """Per-customer metrics: first/last purchase, sales count, revenue, average
        receipt, returns, balance (debt), profit. Base currency."""
        filters = ["balance>0"] if only_debtors else []
        rows = await api.get_counterparty_report(filters, 0)
        return as_object(aggregate.counterparty_report(rows, clamp_limit(limit)))

    @mcp.tool()
    async def get_money(
        date_from: Annotated[str, Field(description="Period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Period end YYYY-MM-DD.")] = "",
        interval: Annotated[
            Literal["hour", "day", "month"], Field(description="Series interval. Default day.")
        ] = "day",
    ) -> dict[str, Any]:
        """Cash flow over a period: money in, out, net, and a time series. Base currency."""
        return as_object(aggregate.money(await api.get_money_series(date_from, date_to, interval)))

    # ---- analytics --------------------------------------------------------

    @mcp.tool()
    async def compare_periods(
        period_a_from: Annotated[str, Field(description="Baseline start YYYY-MM-DD.")],
        period_a_to: Annotated[str, Field(description="Baseline end YYYY-MM-DD.")],
        period_b_from: Annotated[str, Field(description="Comparison start YYYY-MM-DD.")],
        period_b_to: Annotated[str, Field(description="Comparison end YYYY-MM-DD.")],
        dimension: Annotated[
            Literal["product", "counterparty"], Field(description="Default product.")
        ] = "product",
        metric: Annotated[
            Literal["revenue", "profit"], Field(description="Default revenue.")
        ] = "revenue",
        top_n: Annotated[int, Field(description="Top gainers/decliners. Default 10.")] = 10,
    ) -> dict[str, Any]:
        """Compare two periods on revenue or profit and surface the biggest gainers and
        decliners by product or customer. Base currency."""
        if top_n <= 0:
            top_n = 10
        opts_a = ProfitOptions(from_=period_a_from, to=period_a_to)
        opts_b = ProfitOptions(from_=period_b_from, to=period_b_to)
        if dimension == "counterparty":
            a = await api.profit_by_entity("counterparty", opts_a)
            b = await api.profit_by_entity("counterparty", opts_b)
            cmp = aggregate.compare_periods(
                _entity_pairs(a, metric), _entity_pairs(b, metric), top_n
            )
        else:
            a_p = await api.profit_by_product(False, opts_a)
            b_p = await api.profit_by_product(False, opts_b)
            cmp = aggregate.compare_periods(
                _product_pairs(a_p, metric), _product_pairs(b_p, metric), top_n
            )
        return as_object(cmp)

    @mcp.tool()
    async def abc_analysis(
        dimension: Annotated[
            Literal["product", "counterparty"], Field(description="Default product.")
        ] = "product",
        metric: Annotated[
            Literal["revenue", "profit"], Field(description="Default revenue.")
        ] = "revenue",
        date_from: Annotated[str, Field(description="Period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Period end YYYY-MM-DD.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """ABC (Pareto) analysis of products or customers by revenue or profit. A = top
        ~80% of value, B = next ~15%, C = long tail. Base currency."""
        opts = ProfitOptions(from_=date_from, to=date_to)
        display = clamp_limit(limit)
        if dimension == "counterparty":
            rows = await api.profit_by_entity("counterparty", opts)
            items = aggregate.abc(_entity_pairs(rows, metric))
        else:
            product_rows = await api.profit_by_product(False, opts)
            items = aggregate.abc(_product_pairs(product_rows, metric))
        return as_object(aggregate.abc_report(items, display))

    @mcp.tool()
    async def segment_counterparties(
        sleeping_days: Annotated[int, Field(description="No purchase > this ⇒ sleeping.")] = 90,
        at_risk_days: Annotated[int, Field(description="Purchase gap ⇒ at_risk.")] = 45,
        vip_top_percent: Annotated[float, Field(description="Top revenue share ⇒ vip.")] = 0.2,
        low_check_threshold: Annotated[
            float, Field(description="Avg receipt below this ⇒ low_check. 0 = off.")
        ] = 0.0,
        limit: Annotated[int, Field(description="Max detail rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """Rule-based customer segmentation: vip, sleeping, at_risk, low_check, debtor,
        negative_margin. Heuristic, not predictive. Base currency."""
        rows = await api.get_counterparty_report([], 0)
        params = SegmentParams(
            now=now(),
            sleeping_days=sleeping_days,
            at_risk_days=at_risk_days,
            vip_top_percent=vip_top_percent,
            low_check_threshold=low_check_threshold,
        )
        segs = aggregate.segment_counterparties(rows, params)
        return as_object(aggregate.segment_report(segs, clamp_limit(limit)))

    @mcp.tool()
    async def dead_stock(
        threshold_days: Annotated[int, Field(description="Min age on warehouse. Default 90.")] = 90,
        date_from: Annotated[str, Field(description="Movement window start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Movement window end YYYY-MM-DD.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Dead/slow stock: items on hand at least threshold_days with no outbound
        movement in the period, sorted by tied-up value."""
        if threshold_days < 0:
            threshold_days = 90
        stock_rows = await api.get_stock(
            StockOptions(stock_mode="positiveOnly", group_by="product")
        )
        outcome_by_ref: dict[str, float] | None = None
        if date_from or date_to:
            turns = await api.get_turnover(ProfitOptions(from_=date_from, to=date_to))
            outcome_by_ref = {t.assortment.meta.href: t.outcome.quantity for t in turns}
        lines = aggregate.dead_stock(stock_rows, outcome_by_ref, threshold_days)
        return as_object(aggregate.dead_stock_report(lines, clamp_limit(limit)))

    @mcp.tool()
    async def receivables_aging(
        limit: Annotated[int, Field(description="Max per-invoice rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """Accounts-receivable aging from customer invoices: total outstanding/overdue
        and buckets (current, 1-30, 31-60, 61-90, 90+). Base currency."""
        docs = await api.search_documents(
            DocumentType.INVOICE_OUT, DocumentQuery(expand=["agent"], order="moment,desc")
        )
        return as_object(aggregate.receivables_aging(docs, now(), clamp_limit(limit)))

    # ---- documents & counterparties --------------------------------------

    @mcp.tool()
    async def search_documents(
        type: DocTypeArg,
        date_from: Annotated[str, Field(description="moment >= YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="moment <= YYYY-MM-DD.")] = "",
        counterparty_id: Annotated[str, Field(description="Filter by counterparty UUID.")] = "",
        search: Annotated[str, Field(description="Free-text over name/description.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Search documents of a type in a date range. `totals` (sum, paid) cover every
        match; `rows` are the most recent. Use get_document for line items."""
        query = DocumentQuery(
            from_=date_from,
            to=date_to,
            counterparty_id=counterparty_id,
            search=search,
            order="moment,desc",
        )
        if has_currency(type):
            query.expand = ["rate.currency"]
        docs = await api.search_documents(type, query)
        return as_object(aggregate.document_report(docs, clamp_limit(limit)))

    @mcp.tool()
    async def get_document(
        type: DocTypeArg,
        id: Annotated[str, Field(description="Document UUID.")],
    ) -> dict[str, Any]:
        """Fetch one document by type and id with its line items (product, quantity,
        price, discount, total). Base currency."""
        expand = ["positions.assortment", "agent", "state", "store"]
        if has_currency(type):
            expand.append("rate.currency")
        doc = await api.get_document(type, id, expand)
        return as_object(aggregate.document_detail_of(doc))

    @mcp.tool()
    async def search_counterparty(
        query: Annotated[str, Field(description="Full-text search term.")] = "",
        limit: Annotated[int, Field(description="Max rows. Default 50.")] = 50,
    ) -> dict[str, Any]:
        """Find counterparties (customers/suppliers) by name, INN, phone or email.
        Returns id, name, type, INN, contacts — use the id to filter other tools."""
        rows = await api.search_counterparties(
            ListOptions(search=query, limit=clamp_limit(limit), order="name,asc")
        )
        return as_object(rows)

    @mcp.tool()
    async def get_account_currency() -> dict[str, Any]:
        """The account's base (accounting) currency — what every amount from the other
        tools is denominated in. Returns isoCode (RUB, MDL, EUR) and short name."""
        cur = await api.account_currency()
        return {
            "isoCode": cur.iso_code,
            "name": cur.name,
            "fullName": cur.full_name,
            "code": cur.code,
        }

    return mcp
