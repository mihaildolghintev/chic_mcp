"""Async HTTP client for the MoySklad JSON API 1.2.

Handles static Bearer-token auth, the account rate limit (45 requests / 3 s),
retry with backoff on network errors / 429 / 5xx (honoring the MoySklad-specific
``X-Lognex-Retry-After`` header, which is in **milliseconds**), and offset/limit
pagination. Amounts stay in minor units; conversion happens in the aggregation
layer.
"""

from __future__ import annotations

from collections.abc import Awaitable, Callable
from urllib.parse import quote

import httpx
from aiolimiter import AsyncLimiter

from chic.moysklad.dates import normalize_moment, normalize_moment_end
from chic.moysklad.documents import DocumentType, valid_document_type
from chic.moysklad.errors import MoyskladError, parse_api_error
from chic.moysklad.models import (
    Counterparty,
    CounterpartyRow,
    Currency,
    Dashboard,
    Document,
    ListResponse,
    MoneySeries,
    Product,
    ProfitByEntityRow,
    ProfitByProductRow,
    StockRow,
    TurnoverRow,
)
from chic.moysklad.options import (
    DocumentQuery,
    ListOptions,
    ProfitOptions,
    QueryParams,
    StockOptions,
)
from chic.tracing import http_span, record_http_response

DEFAULT_BASE_URL = "https://api.moysklad.ru/api/remap/1.2"
DEFAULT_PAGE_LIMIT = 1000

SleepFn = Callable[[float], Awaitable[None]]


async def _default_sleep(seconds: float) -> None:
    import asyncio

    await asyncio.sleep(seconds)


class MoyskladClient:
    """A MoySklad API client. Safe for concurrent use."""

    def __init__(
        self,
        token: str,
        *,
        base_url: str = DEFAULT_BASE_URL,
        http: httpx.AsyncClient | None = None,
        max_retries: int = 3,
        base_delay: float = 0.5,
        page_limit: int = DEFAULT_PAGE_LIMIT,
        limiter: AsyncLimiter | None = None,
        user_agent: str = "mcp-moysklad/0.1",
        sleep: SleepFn | None = None,
    ) -> None:
        self._token = token
        self._base_url = base_url
        self._http = http or httpx.AsyncClient(timeout=30.0)
        self._owns_http = http is None
        self._max_retries = max_retries
        self._base_delay = base_delay
        self._page_limit = page_limit if page_limit > 0 else DEFAULT_PAGE_LIMIT
        # 45 requests / 3 seconds — MoySklad's documented account limit.
        self._limiter = limiter or AsyncLimiter(45, 3)
        self._user_agent = user_agent
        self._sleep = sleep or _default_sleep

    async def aclose(self) -> None:
        if self._owns_http:
            await self._http.aclose()

    # ---- transport --------------------------------------------------------

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {self._token}",
            "Accept": "application/json;charset=utf-8",
            "User-Agent": self._user_agent,
        }

    async def _do_get(self, path: str, params: QueryParams) -> bytes:
        url = self._base_url + path
        last_err: Exception | None = None
        with http_span("GET", path, url, params) as span:
            for attempt in range(self._max_retries + 1):
                try:
                    async with self._limiter:
                        # httpx's param type is invariant and rejects list[tuple[str, str]].
                        resp = await self._http.get(
                            url,
                            params=params,  # type: ignore[arg-type]
                            headers=self._headers(),
                        )
                except httpx.HTTPError as exc:
                    last_err = exc
                    await self._backoff(attempt, 0.0)
                    continue

                body = resp.content
                status = resp.status_code
                record_http_response(span, status, body)
                if 200 <= status < 300:
                    return body
                if status == 429:
                    last_err = parse_api_error(status, body)
                    if attempt == self._max_retries:
                        raise last_err
                    await self._backoff(attempt, _retry_after(resp.headers))
                    continue
                if status >= 500:
                    last_err = parse_api_error(status, body)
                    if attempt == self._max_retries:
                        raise last_err
                    await self._backoff(attempt, 0.0)
                    continue
                raise parse_api_error(status, body)

            if last_err is None:  # unreachable: the loop always sets last_err before exhausting
                raise RuntimeError("moysklad: request failed without an error")
            raise last_err

    async def _backoff(self, attempt: int, retry_after: float) -> None:
        delay = retry_after if retry_after > 0 else self._base_delay * (2**attempt)
        if delay > 0:
            await self._sleep(delay)

    async def _paginate[T](
        self,
        path: str,
        base_params: QueryParams,
        total_limit: int,
        parse: Callable[[bytes], ListResponse[T]],
    ) -> list[T]:
        page_size = self._page_limit
        if 0 < total_limit < page_size:
            page_size = total_limit

        rows: list[T] = []
        offset = 0
        while True:
            params = [*base_params, ("limit", str(page_size)), ("offset", str(offset))]
            page = parse(await self._do_get(path, params))
            rows.extend(page.rows)
            if len(page.rows) < page_size:
                break
            if total_limit > 0 and len(rows) >= total_limit:
                rows = rows[:total_limit]
                break
            offset += page_size
            if page.meta.size > 0 and offset >= page.meta.size:
                break
        return rows

    # ---- entity endpoints -------------------------------------------------

    async def list_products(self, opts: ListOptions) -> list[Product]:
        return await self._paginate(
            "/entity/product",
            opts.values(),
            opts.limit,
            lambda b: ListResponse[Product].model_validate_json(b),
        )

    async def search_counterparties(self, opts: ListOptions) -> list[Counterparty]:
        return await self._paginate(
            "/entity/counterparty",
            opts.values(),
            opts.limit,
            lambda b: ListResponse[Counterparty].model_validate_json(b),
        )

    async def account_currency(self) -> Currency:
        rows = await self._paginate(
            "/entity/currency",
            [("filter", "default=true")],
            0,
            lambda b: ListResponse[Currency].model_validate_json(b),
        )
        for row in rows:
            if row.default:
                return row
        # Some accounts don't honor the filter; fall back to the first row.
        if rows:
            return rows[0]
        raise MoyskladError(0, "no account currency found")

    # ---- report endpoints -------------------------------------------------

    async def get_dashboard(self, period: str) -> Dashboard:
        body = await self._do_get(f"/report/dashboard/{period}", [])
        return Dashboard.model_validate_json(body)

    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]:
        path = "/report/profit/byvariant" if variant else "/report/profit/byproduct"
        return await self._paginate(
            path,
            opts.values(),
            opts.limit,
            lambda b: ListResponse[ProfitByProductRow].model_validate_json(b),
        )

    async def profit_by_entity(
        self, dimension: str, opts: ProfitOptions
    ) -> list[ProfitByEntityRow]:
        return await self._paginate(
            f"/report/profit/by{dimension}",
            opts.values(),
            opts.limit,
            lambda b: ListResponse[ProfitByEntityRow].model_validate_json(b),
        )

    async def get_turnover(self, opts: ProfitOptions) -> list[TurnoverRow]:
        return await self._paginate(
            "/report/turnover/all",
            opts.values(),
            opts.limit,
            lambda b: ListResponse[TurnoverRow].model_validate_json(b),
        )

    async def get_stock(self, opts: StockOptions) -> list[StockRow]:
        params = opts.values()
        if opts.store_id:
            params.append(("filter", f"store={self._base_url}/entity/store/{opts.store_id}"))
        return await self._paginate(
            "/report/stock/all",
            params,
            opts.limit,
            lambda b: ListResponse[StockRow].model_validate_json(b),
        )

    async def get_counterparty_report(
        self, filters: list[str], limit: int
    ) -> list[CounterpartyRow]:
        params: QueryParams = [("filter", f) for f in filters]
        return await self._paginate(
            "/report/counterparty",
            params,
            limit,
            lambda b: ListResponse[CounterpartyRow].model_validate_json(b),
        )

    async def get_money_series(self, date_from: str, date_to: str, interval: str) -> MoneySeries:
        params: QueryParams = []
        if m := normalize_moment(date_from):
            params.append(("momentFrom", m))
        if m := normalize_moment_end(date_to):
            params.append(("momentTo", m))
        if interval:
            params.append(("interval", interval))
        body = await self._do_get("/report/money/plotseries", params)
        return MoneySeries.model_validate_json(body)

    # ---- documents --------------------------------------------------------

    async def search_documents(
        self, doc_type: DocumentType | str, query: DocumentQuery
    ) -> list[Document]:
        if not valid_document_type(str(doc_type)):
            raise ValueError(f"moysklad: unsupported document type {doc_type!r}")
        return await self._paginate(
            f"/entity/{doc_type}",
            query.values(self._base_url),
            query.limit,
            lambda b: ListResponse[Document].model_validate_json(b),
        )

    async def get_document(
        self, doc_type: DocumentType | str, doc_id: str, expand: list[str]
    ) -> Document:
        if not valid_document_type(str(doc_type)):
            raise ValueError(f"moysklad: unsupported document type {doc_type!r}")
        params: QueryParams = []
        if expand:
            params.append(("expand", ",".join(expand)))
        # Escape the id so it can't inject extra path/query segments.
        path = f"/entity/{doc_type}/{quote(doc_id, safe='')}"
        body = await self._do_get(path, params)
        return Document.model_validate_json(body)


def _retry_after(headers: httpx.Headers) -> float:
    """Wait seconds from MoySklad's rate-limit headers.

    ``X-Lognex-Retry-After`` is milliseconds (MoySklad-specific); the standard
    ``Retry-After`` is seconds.
    """
    v = headers.get("X-Lognex-Retry-After")
    if v:
        try:
            return int(v) / 1000.0
        except ValueError:
            pass
    v = headers.get("Retry-After")
    if v:
        try:
            return float(int(v))
        except ValueError:
            pass
    return 0.0
