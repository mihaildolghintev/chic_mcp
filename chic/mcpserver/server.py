"""MoySklad analytics exposed as MCP tools (FastMCP).

Every tool is read-only. `build_server(api, history)` registers the tools, grouped
by domain into `_register_*` functions, closing over the given MoySklad client
(raw or cache-wrapped) and optional snapshot store. Tool bodies are thin: they
delegate the fetch-and-aggregate orchestration to `chic.usecases` and wrap the
result for the LLM. Reports return the uniform `{totals, rowCount, returned,
truncated, rows}` envelope so the model uses totals rather than re-summing
possibly-truncated rows.
"""

from __future__ import annotations

from decimal import Decimal
from typing import Annotated, Any, Literal

from mcp.server.fastmcp import FastMCP
from pydantic import Field

from chic import aggregate, usecases
from chic.aggregate.analytics import SegmentParams
from chic.aggregate.report import profit_by_entity, profit_by_product
from chic.cache import Source
from chic.history import HistoryStore
from chic.mcpserver.helpers import (
    as_object,
    clamp_limit,
    ensure_ymd,
    now,
    period_days,
    prev_month,
)
from chic.moysklad import (
    DocumentQuery,
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
    "internalorder",
    "invoiceout",
    "invoicein",
    "salesreturn",
    "purchasereturn",
    "paymentin",
    "paymentout",
    "cashin",
    "cashout",
    "commissionreportin",
    "commissionreportout",
    "factureout",
    "facturein",
    "counterpartyadjustment",
    "prepayment",
    "prepaymentreturn",
    "retaildemand",
    "retailsalesreturn",
    "retailshift",
    "retaildrawercashin",
    "retaildrawercashout",
    "move",
    "inventory",
    "loss",
    "enter",
    "processing",
]

# Document types that own a status workflow (states via metadata). Used by both
# search_documents (state filter) and list_document_states.
StatefulDocTypeArg = Literal[
    "demand",
    "customerorder",
    "supply",
    "purchaseorder",
    "internalorder",
    "invoiceout",
    "invoicein",
    "salesreturn",
    "purchasereturn",
    "paymentin",
    "paymentout",
    "cashin",
    "cashout",
    "commissionreportin",
    "commissionreportout",
    "counterpartyadjustment",
    "retaildemand",
    "move",
    "inventory",
    "loss",
    "enter",
    "processing",
]

ReferenceKindArg = Literal[
    "store",
    "organization",
    "saleschannel",
    "employee",
    "project",
    "expenseitem",
    "productfolder",
    "contract",
    "group",
    "country",
    "uom",
]

# Reference dictionaries that carry an ``archived`` flag. The rest (country, uom,
# group) have no such attribute, and filtering by an unknown field is a hard 400.
_ARCHIVABLE_REFERENCES: frozenset[str] = frozenset(
    {
        "store",
        "organization",
        "saleschannel",
        "employee",
        "project",
        "expenseitem",
        "productfolder",
        "contract",
    }
)


def _product_pairs(rows: list[ProfitByProductRow], metric: str) -> list[tuple[str, Decimal]]:
    return [
        (x.name, x.profit if metric == "profit" else x.revenue) for x in profit_by_product(rows)
    ]


def _entity_pairs(rows: list[ProfitByEntityRow], metric: str) -> list[tuple[str, Decimal]]:
    return [(x.name, x.profit if metric == "profit" else x.revenue) for x in profit_by_entity(rows)]


def build_server(api: Source, history: HistoryStore | None = None) -> FastMCP:
    """Register every read-only MoySklad tool, grouped by domain. History-powered
    tools are added only when a snapshot store is wired."""
    mcp = FastMCP("moysklad-mcp")
    _register_reports(mcp, api)
    _register_analytics(mcp, api)
    _register_catalog(mcp, api)
    if history is not None:
        _register_history_tools(mcp, api, history)
    return mcp


def _register_reports(mcp: FastMCP, api: Source) -> None:
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
        return as_object(await usecases.dashboard(api, period))

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


def _register_analytics(mcp: FastMCP, api: Source) -> None:
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
    async def explain_profit_change(
        period_a_from: Annotated[str, Field(description="Baseline start YYYY-MM-DD.")],
        period_a_to: Annotated[str, Field(description="Baseline end YYYY-MM-DD.")],
        period_b_from: Annotated[str, Field(description="Comparison start YYYY-MM-DD.")],
        period_b_to: Annotated[str, Field(description="Comparison end YYYY-MM-DD.")],
        top_n: Annotated[int, Field(description="Top product drivers to return. Default 10.")] = 10,
    ) -> dict[str, Any]:
        """Explain WHY gross profit changed between two periods: decompose the delta
        into price, volume, mix and cost effects (plus new/discontinued products), and
        list the products driving it. Effects reconcile to the total delta to the cent.
        Gross profit is revenue − cost per product. Base currency."""
        if top_n <= 0:
            top_n = 10
        bridge = await usecases.profit_bridge(
            api, period_a_from, period_a_to, period_b_from, period_b_to, top_n
        )
        return as_object(bridge)

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
        return as_object(await usecases.receivables(api, now(), clamp_limit(limit)))


def _register_catalog(mcp: FastMCP, api: Source) -> None:
    @mcp.tool()
    async def search_documents(
        type: DocTypeArg,
        date_from: Annotated[str, Field(description="moment >= YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="moment <= YYYY-MM-DD.")] = "",
        counterparty_id: Annotated[str, Field(description="Filter by counterparty UUID.")] = "",
        organization_id: Annotated[str, Field(description="Filter by own-company UUID.")] = "",
        store_id: Annotated[str, Field(description="Filter by warehouse UUID.")] = "",
        state_id: Annotated[
            str, Field(description="Filter by document status UUID (see list_document_states).")
        ] = "",
        search: Annotated[str, Field(description="Free-text over name/description.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Search documents of a type in a date range, optionally scoped by counterparty,
        organization, warehouse or status. `totals` (sum, paid) cover every match; `rows`
        are the most recent. Covers retail sales, cash orders, commission reports, etc.
        Use get_document for line items."""
        query = DocumentQuery(
            from_=date_from,
            to=date_to,
            counterparty_id=counterparty_id,
            organization_id=organization_id,
            store_id=store_id,
            state_id=state_id,
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

    # ---- reference dictionaries & catalog --------------------------------

    @mcp.tool()
    async def list_references(
        kind: Annotated[
            ReferenceKindArg,
            Field(
                description="Which dictionary: store, organization, saleschannel, employee, "
                "project, expenseitem, productfolder, contract, group, country, uom."
            ),
        ],
        query: Annotated[str, Field(description="Full-text search over the dictionary.")] = "",
        include_archived: Annotated[bool, Field(description="Include archived rows.")] = False,
        limit: Annotated[int, Field(description="Max rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """List a reference dictionary (id, name, code). Use this to resolve the UUIDs
        that other tools accept — e.g. a warehouse for get_stock/get_stock_by_store, a
        sales channel/employee for get_profit, or an organization/store for
        search_documents and get_sales."""
        opts = ListOptions(search=query, limit=clamp_limit(limit), order="name,asc")
        if not include_archived and kind in _ARCHIVABLE_REFERENCES:
            opts.filter.append("archived=false")
        return as_object(aggregate.entity_refs(await api.list_entity_refs(kind, opts)))

    @mcp.tool()
    async def list_document_states(
        document_type: Annotated[
            StatefulDocTypeArg, Field(description="Document type whose statuses to list.")
        ],
    ) -> dict[str, Any]:
        """List the status workflow (id, name, type) for a document type. Feed a status
        id to search_documents(state_id=...) to filter, e.g. orders in a given stage."""
        return as_object(aggregate.states(await api.list_states(document_type)))

    @mcp.tool()
    async def search_assortment(
        query: Annotated[str, Field(description="Full-text over name/code/article.")] = "",
        include_archived: Annotated[bool, Field(description="Include archived items.")] = False,
        limit: Annotated[int, Field(description="Max rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Unified catalog search across products, variants, bundles and services (with
        `kind` telling them apart). Broader than list_products, which is products-only."""
        opts = ListOptions(search=query, limit=clamp_limit(limit), order="name,asc")
        if not include_archived:
            opts.filter.append("archived=false")
        return as_object(aggregate.assortment(await api.search_assortment(opts)))

    # ---- extra reports ----------------------------------------------------

    @mcp.tool()
    async def get_stock_by_store(
        stock_mode: Annotated[
            Literal["nonEmpty", "all", "positiveOnly", "negativeOnly", "empty"],
            Field(description="Stock filter. Default nonEmpty."),
        ] = "nonEmpty",
        date: Annotated[str, Field(description="Stock as of this date YYYY-MM-DD.")] = "",
    ) -> dict[str, Any]:
        """Stock split by warehouse: units, reserved and available per store (a
        where-is-my-inventory summary). Use get_stock for per-product cost/value."""
        rows = await api.get_stock_by_store(
            StockOptions(stock_mode=stock_mode, group_by="product", moment=date)
        )
        return as_object(aggregate.stock_by_store(rows))

    @mcp.tool()
    async def get_sales(
        kind: Annotated[
            Literal["sales", "orders"],
            Field(description="sales = shipped demand; orders = customer orders. Default sales."),
        ] = "sales",
        date_from: Annotated[str, Field(description="Period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="Period end YYYY-MM-DD.")] = "",
        interval: Annotated[
            Literal["hour", "day", "month"], Field(description="Series interval. Default day.")
        ] = "day",
        store_id: Annotated[str, Field(description="Scope to a warehouse UUID.")] = "",
        organization_id: Annotated[str, Field(description="Scope to an own-company UUID.")] = "",
        project_id: Annotated[str, Field(description="Scope to a project UUID.")] = "",
    ) -> dict[str, Any]:
        """Sales or orders as a time series: quantity and amount per interval, plus totals.
        No dates ⇒ previous full month. Base currency."""
        if not date_from and not date_to:
            date_from, date_to = prev_month(now())
        series = await api.get_sales_series(
            kind,
            date_from,
            date_to,
            interval,
            store_id=store_id,
            organization_id=organization_id,
            project_id=project_id,
        )
        return as_object(aggregate.sales_series(kind, series))

    @mcp.tool()
    async def get_audit(
        entity_type: Annotated[
            str, Field(description="Filter by entity type, e.g. customerorder, demand, product.")
        ] = "",
        event_type: Annotated[
            str, Field(description="Filter by event: create, update, delete.")
        ] = "",
        limit: Annotated[int, Field(description="Max rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """Account change log: who changed what and when (moment, employee, entity/event
        type). Newest first. Use to answer 'what changed recently'."""
        filters: list[str] = []
        if entity_type:
            filters.append(f"entityType={entity_type}")
        if event_type:
            filters.append(f"eventType={event_type}")
        rows = await api.get_audit(filters, clamp_limit(limit))
        return as_object(aggregate.audit(rows))

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


def _register_history_tools(mcp: FastMCP, api: Source, history: HistoryStore) -> None:
    """Register tools that read the local snapshot history (XYZ, ABC/XYZ)."""

    @mcp.tool()
    async def xyz_analysis(
        min_days: Annotated[
            int, Field(description="Min days of history to classify. Default 3.")
        ] = 3,
        x_threshold: Annotated[float, Field(description="CV ≤ this ⇒ X (stable).")] = 0.10,
        y_threshold: Annotated[float, Field(description="CV ≤ this ⇒ Y (variable).")] = 0.25,
        since: Annotated[str, Field(description="Only history since this date YYYY-MM-DD.")] = "",
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """XYZ analysis of demand predictability from local snapshot history: the
        coefficient of variation (σ/μ) of each product's daily sales. X = stable,
        Y = variable, Z = erratic. Needs accumulated history; `unclassified` counts
        products with too few days yet."""
        report = await usecases.xyz(
            history,
            since=ensure_ymd(since, "since") or None,
            min_days=max(1, min_days),
            x_threshold=x_threshold,
            y_threshold=y_threshold,
            limit=clamp_limit(limit),
        )
        return as_object(report)

    @mcp.tool()
    async def abc_xyz_matrix(
        metric: Annotated[
            Literal["revenue", "profit"], Field(description="ABC metric. Default revenue.")
        ] = "revenue",
        date_from: Annotated[str, Field(description="ABC period start YYYY-MM-DD.")] = "",
        date_to: Annotated[str, Field(description="ABC period end YYYY-MM-DD.")] = "",
        min_days: Annotated[int, Field(description="Min days of history for XYZ. Default 3.")] = 3,
        limit: Annotated[int, Field(description="Max detail rows. Default 200.")] = 200,
    ) -> dict[str, Any]:
        """ABC/XYZ matrix: cross value (ABC over a period) with demand predictability
        (XYZ over snapshot history). Returns 3×3 cells with counts, revenue and a
        stock recommendation (AX = hold always … CZ = candidate to drop), plus
        `abcOnly` for products lacking history."""
        matrix = await usecases.abc_xyz(
            api,
            history,
            metric=metric,
            date_from=ensure_ymd(date_from, "date_from"),
            date_to=ensure_ymd(date_to, "date_to"),
            min_days=max(1, min_days),
            limit=clamp_limit(limit),
        )
        return as_object(matrix)

    @mcp.tool()
    async def purchase_plan(
        lead_time_days: Annotated[
            float, Field(description="Supplier lead time in days. Default 7.")
        ] = 7.0,
        service_level: Annotated[
            float, Field(description="Target no-stockout probability 0.5–0.999. Default 0.95.")
        ] = 0.95,
        review_period_days: Annotated[
            float, Field(description="Days of extra cover to order up to. Default 7.")
        ] = 7.0,
        fallback_cv: Annotated[
            float, Field(description="Assumed demand CV when history is thin. Default 0.6.")
        ] = 0.6,
        limit: Annotated[int, Field(description="Max detail rows. Default 100.")] = 100,
    ) -> dict[str, Any]:
        """What to order now and days of cover left, per product. Safety stock =
        z(service_level)·σ·√lead; reorder when available (stock − reserve + in-transit)
        falls below the reorder point. Demand comes from snapshot history where
        available (source=history) else a recent-sales estimate (source=estimate)."""
        report = await usecases.purchase(
            api,
            history,
            now(),
            lead_time_days=lead_time_days,
            service_level=service_level,
            review_period_days=review_period_days,
            fallback_cv=fallback_cv,
            limit=clamp_limit(limit),
        )
        return as_object(report)
