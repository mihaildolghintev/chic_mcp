"""Shared helpers for the MCP tools."""

from __future__ import annotations

from datetime import datetime, timedelta
from typing import Any

from pydantic import BaseModel

from chic.aggregate.money import parse_time


def now() -> datetime:
    return datetime.now()


def clamp_limit(n: int) -> int:
    """Bound a requested row limit to [1, 1000], defaulting to 100."""
    if n <= 0:
        return 100
    if n > 1000:
        return 1000
    return n


def prev_month(ref: datetime) -> tuple[str, str]:
    """Previous full calendar month as (from, to) YYYY-MM-DD."""
    first_this = ref.replace(day=1, hour=0, minute=0, second=0, microsecond=0)
    end = first_this - timedelta(days=1)
    start = end.replace(day=1)
    return start.strftime("%Y-%m-%d"), end.strftime("%Y-%m-%d")


def _normalize(s: str) -> str:
    if not s:
        return ""
    return f"{s} 00:00:00" if len(s) == 10 else s


def period_days(date_from: str, date_to: str, fallback: float) -> float:
    """Inclusive day count between two MoySklad dates, or fallback if unparseable."""
    f = parse_time(_normalize(date_from))
    t = parse_time(_normalize(date_to))
    if f is None or t is None:
        return fallback
    # +1 because the range is inclusive of both endpoints.
    d = (t - f).total_seconds() / 3600 / 24 + 1
    return d if d > 0 else fallback


def as_object(v: object) -> dict[str, Any]:
    """Guarantee a JSON object result (MCP rejects a top-level array).

    Pydantic models are dumped by alias; lists are wrapped as {items, count}.
    """
    if isinstance(v, BaseModel):
        return v.model_dump(by_alias=True)
    if isinstance(v, list):
        return {
            "items": [i.model_dump(by_alias=True) if isinstance(i, BaseModel) else i for i in v],
            "count": len(v),
        }
    if isinstance(v, dict):
        return v
    raise TypeError(f"cannot render {type(v)!r} as a JSON object")
