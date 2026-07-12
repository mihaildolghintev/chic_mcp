"""Query-parameter options for the MoySklad client.

Each option object builds a list of ``(key, value)`` query tuples (repeated
``filter`` params are allowed). They are pydantic models so the cache layer can
serialize them into a stable key.
"""

from __future__ import annotations

from pydantic import BaseModel

from chic.moysklad.dates import normalize_moment, normalize_moment_end

QueryParams = list[tuple[str, str]]


class ListOptions(BaseModel):
    search: str = ""
    filter: list[str] = []
    expand: list[str] = []
    limit: int = 0  # 0 ⇒ all pages
    order: str = ""

    def values(self) -> QueryParams:
        v: QueryParams = []
        if self.search:
            v.append(("search", self.search))
        v.extend(("filter", f) for f in self.filter)
        if self.expand:
            v.append(("expand", ",".join(self.expand)))
        if self.order:
            v.append(("order", self.order))
        return v


class ProfitOptions(BaseModel):
    from_: str = ""
    to: str = ""
    filter: list[str] = []
    limit: int = 0

    def values(self) -> QueryParams:
        v: QueryParams = []
        if m := normalize_moment(self.from_):
            v.append(("momentFrom", m))
        if m := normalize_moment_end(self.to):
            v.append(("momentTo", m))
        v.extend(("filter", f) for f in self.filter)
        return v


class StockOptions(BaseModel):
    stock_mode: str = ""  # all|positiveOnly|negativeOnly|empty|nonEmpty|underMinimum
    group_by: str = ""  # product|variant|consignment
    moment: str = ""
    store_id: str = ""
    filter: list[str] = []
    limit: int = 0

    def values(self) -> QueryParams:
        v: QueryParams = []
        if self.stock_mode:
            v.append(("stockMode", self.stock_mode))
        if self.group_by:
            v.append(("groupBy", self.group_by))
        if m := normalize_moment(self.moment):
            v.append(("moment", m))
        v.extend(("filter", f) for f in self.filter)
        return v


class DocumentQuery(BaseModel):
    from_: str = ""  # moment >=
    to: str = ""  # moment <=
    counterparty_id: str = ""  # agent filter by id (href built internally)
    # These are stored as raw UUIDs; the client builds their entity hrefs (the
    # state href needs the document type, which only the client knows). They are
    # deliberately part of the model so the cache key varies with them, but they
    # are NOT emitted by ``values()``.
    state_id: str = ""
    organization_id: str = ""
    store_id: str = ""
    filter: list[str] = []
    search: str = ""
    expand: list[str] = []
    limit: int = 0
    order: str = ""

    def values(self, base_url: str) -> QueryParams:
        v: QueryParams = []
        filters: list[str] = []
        if m := normalize_moment(self.from_):
            filters.append(f"moment>={m}")
        if m := normalize_moment_end(self.to):
            filters.append(f"moment<={m}")
        if self.counterparty_id:
            filters.append(f"agent={base_url}/entity/counterparty/{self.counterparty_id}")
        filters.extend(self.filter)
        v.extend(("filter", f) for f in filters)
        if self.search:
            v.append(("search", self.search))
        if self.expand:
            v.append(("expand", ",".join(self.expand)))
        if self.order:
            v.append(("order", self.order))
        return v
