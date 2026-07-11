"""Caching decorator around the MoySklad client.

Implements the same method surface as :class:`chic.moysklad.MoyskladClient`, so a
``CachingClient`` is a drop-in wherever the raw client is used. Per-call-class
TTLs; a zero TTL disables caching for that class.
"""

from __future__ import annotations

import dataclasses
import hashlib
import json
import logging
from collections.abc import Awaitable, Callable
from typing import Any, Protocol

from pydantic import BaseModel, TypeAdapter, ValidationError

from chic.cache.store import CacheStore
from chic.moysklad.documents import DocumentType
from chic.moysklad.models import (
    Counterparty,
    CounterpartyRow,
    Currency,
    Dashboard,
    Document,
    MoneySeries,
    Product,
    ProfitByEntityRow,
    ProfitByProductRow,
    StockRow,
    TurnoverRow,
)
from chic.moysklad.options import DocumentQuery, ListOptions, ProfitOptions, StockOptions

logger = logging.getLogger(__name__)


class Source(Protocol):
    """The MoySklad surface the cache wraps (structurally satisfied by the client)."""

    async def list_products(self, opts: ListOptions) -> list[Product]: ...
    async def search_counterparties(self, opts: ListOptions) -> list[Counterparty]: ...
    async def account_currency(self) -> Currency: ...
    async def get_dashboard(self, period: str) -> Dashboard: ...
    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]: ...
    async def profit_by_entity(
        self, dimension: str, opts: ProfitOptions
    ) -> list[ProfitByEntityRow]: ...
    async def get_turnover(self, opts: ProfitOptions) -> list[TurnoverRow]: ...
    async def get_stock(self, opts: StockOptions) -> list[StockRow]: ...
    async def get_counterparty_report(
        self, filters: list[str], limit: int
    ) -> list[CounterpartyRow]: ...
    async def get_money_series(
        self, date_from: str, date_to: str, interval: str
    ) -> MoneySeries: ...
    async def search_documents(
        self, doc_type: DocumentType | str, query: DocumentQuery
    ) -> list[Document]: ...
    async def get_document(
        self, doc_type: DocumentType | str, doc_id: str, expand: list[str]
    ) -> Document: ...


@dataclasses.dataclass(frozen=True)
class TTLs:
    """Cache lifetime (seconds) per call class. Zero disables that class."""

    products: float = 30 * 60
    dashboard: float = 5 * 60
    reports: float = 10 * 60  # profit, turnover, stock, counterparty, money
    documents: float = 5 * 60
    counterparty: float = 30 * 60  # entity/counterparty search
    currency: float = 24 * 60 * 60


# Adapters for (de)serializing each return type to/from the cache blob.
_A_PRODUCTS = TypeAdapter(list[Product])
_A_COUNTERPARTIES = TypeAdapter(list[Counterparty])
_A_CURRENCY = TypeAdapter(Currency)
_A_DASHBOARD = TypeAdapter(Dashboard)
_A_PROFIT_PRODUCT = TypeAdapter(list[ProfitByProductRow])
_A_PROFIT_ENTITY = TypeAdapter(list[ProfitByEntityRow])
_A_TURNOVER = TypeAdapter(list[TurnoverRow])
_A_STOCK = TypeAdapter(list[StockRow])
_A_COUNTERPARTY_REPORT = TypeAdapter(list[CounterpartyRow])
_A_MONEY = TypeAdapter(MoneySeries)
_A_DOCUMENTS = TypeAdapter(list[Document])
_A_DOCUMENT = TypeAdapter(Document)


def _json_default(obj: Any) -> Any:
    if isinstance(obj, BaseModel):
        return obj.model_dump(mode="json")
    raise TypeError(f"not JSON-serializable: {type(obj)!r}")


def _key(method: str, params: Any) -> str:
    h = hashlib.sha256()
    h.update(method.encode())
    h.update(b"\0")
    h.update(json.dumps(params, default=_json_default, sort_keys=True).encode())
    return f"{method}:{h.hexdigest()[:16]}"


class CachingClient:
    def __init__(self, src: Source, store: CacheStore, ttls: TTLs | None = None) -> None:
        self._src = src
        self._store = store
        self._ttls = ttls or TTLs()

    async def _cached[T](
        self,
        ttl: float,
        method: str,
        params: Any,
        adapter: TypeAdapter[T],
        fetch: Callable[[], Awaitable[T]],
    ) -> T:
        if ttl <= 0:
            return await fetch()
        key = _key(method, params)
        raw = await self._store.get(key)
        if raw is not None:
            try:
                return adapter.validate_json(raw)
            except ValidationError:
                pass  # stale/incompatible cache entry — refetch
        value = await fetch()
        await self._store.set(key, adapter.dump_json(value), ttl)
        return value

    async def list_products(self, opts: ListOptions) -> list[Product]:
        return await self._cached(
            self._ttls.products,
            "ListProducts",
            opts,
            _A_PRODUCTS,
            lambda: self._src.list_products(opts),
        )

    async def search_counterparties(self, opts: ListOptions) -> list[Counterparty]:
        return await self._cached(
            self._ttls.counterparty,
            "SearchCounterparties",
            opts,
            _A_COUNTERPARTIES,
            lambda: self._src.search_counterparties(opts),
        )

    async def account_currency(self) -> Currency:
        return await self._cached(
            self._ttls.currency,
            "AccountCurrency",
            None,
            _A_CURRENCY,
            lambda: self._src.account_currency(),
        )

    async def get_dashboard(self, period: str) -> Dashboard:
        return await self._cached(
            self._ttls.dashboard,
            "GetDashboard",
            period,
            _A_DASHBOARD,
            lambda: self._src.get_dashboard(period),
        )

    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]:
        return await self._cached(
            self._ttls.reports,
            "ProfitByProduct",
            [variant, opts],
            _A_PROFIT_PRODUCT,
            lambda: self._src.profit_by_product(variant, opts),
        )

    async def profit_by_entity(
        self, dimension: str, opts: ProfitOptions
    ) -> list[ProfitByEntityRow]:
        return await self._cached(
            self._ttls.reports,
            "ProfitByEntity",
            [dimension, opts],
            _A_PROFIT_ENTITY,
            lambda: self._src.profit_by_entity(dimension, opts),
        )

    async def get_turnover(self, opts: ProfitOptions) -> list[TurnoverRow]:
        return await self._cached(
            self._ttls.reports,
            "GetTurnover",
            opts,
            _A_TURNOVER,
            lambda: self._src.get_turnover(opts),
        )

    async def get_stock(self, opts: StockOptions) -> list[StockRow]:
        return await self._cached(
            self._ttls.reports,
            "GetStock",
            opts,
            _A_STOCK,
            lambda: self._src.get_stock(opts),
        )

    async def get_counterparty_report(
        self, filters: list[str], limit: int
    ) -> list[CounterpartyRow]:
        return await self._cached(
            self._ttls.reports,
            "GetCounterpartyReport",
            [filters, limit],
            _A_COUNTERPARTY_REPORT,
            lambda: self._src.get_counterparty_report(filters, limit),
        )

    async def get_money_series(self, date_from: str, date_to: str, interval: str) -> MoneySeries:
        return await self._cached(
            self._ttls.reports,
            "GetMoneySeries",
            [date_from, date_to, interval],
            _A_MONEY,
            lambda: self._src.get_money_series(date_from, date_to, interval),
        )

    async def search_documents(
        self, doc_type: DocumentType | str, query: DocumentQuery
    ) -> list[Document]:
        return await self._cached(
            self._ttls.documents,
            "SearchDocuments",
            [str(doc_type), query],
            _A_DOCUMENTS,
            lambda: self._src.search_documents(doc_type, query),
        )

    async def get_document(
        self, doc_type: DocumentType | str, doc_id: str, expand: list[str]
    ) -> Document:
        return await self._cached(
            self._ttls.documents,
            "GetDocument",
            [str(doc_type), doc_id, expand],
            _A_DOCUMENT,
            lambda: self._src.get_document(doc_type, doc_id, expand),
        )
