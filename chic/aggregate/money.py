"""Money conversion + rounding, faithfully matching the Go implementation.

MoySklad stores every monetary amount in the account currency's minor units
(1/100 of the major unit). All conversion to major units happens in this layer
and nowhere else; the layer is currency-agnostic.

CRITICAL PARITY NOTE: Go's ``math.Round`` rounds half **away from zero**, whereas
Python's built-in ``round`` and numpy/pandas round half **to even** (banker's
rounding). We reproduce Go's behavior explicitly so cents never diverge — this is
also why the analytics layer does not lean on pandas' ``.round()``.
"""

from __future__ import annotations

import math
from datetime import datetime

_TIME_FORMAT = "%Y-%m-%d %H:%M:%S"


def round_half_away(x: float) -> float:
    """Round to the nearest integer, halves away from zero (like Go math.Round)."""
    return math.floor(x + 0.5) if x >= 0 else math.ceil(x - 0.5)


def minor_to_major(minor: float) -> float:
    """Minor units → major units, 2 decimals (every MoySklad currency is 1/100)."""
    return round_half_away(minor) / 100.0


def round2(v: float) -> float:
    """Round to two decimal places, halves away from zero."""
    return round_half_away(v * 100) / 100.0


def margin_pct(profit: float, revenue: float) -> float:
    if revenue == 0:
        return 0.0
    return round2(profit / revenue * 100)


def pct_change(a: float, b: float) -> float:
    if a == 0:
        return 0.0 if b == 0 else 100.0
    return round2((b - a) / a * 100)


def parse_time(s: str) -> datetime | None:
    """Parse a MoySklad timestamp; None for empty/invalid input."""
    if not s:
        return None
    try:
        return datetime.strptime(s, _TIME_FORMAT)  # MoySklad timestamps are naive
    except ValueError:
        return None


def days_between(now: datetime, earlier: datetime) -> int:
    """Whole days from ``earlier`` to ``now`` (truncated toward zero, like Go int())."""
    return int((now - earlier).total_seconds() / 3600 / 24)
