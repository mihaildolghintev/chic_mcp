"""Money conversion + rounding.

MoySklad stores every monetary amount in the account currency's minor units
(1/100 of the major unit). All conversion to major units happens in this layer
and nowhere else; the layer is currency-agnostic.

Money is represented as :class:`decimal.Decimal` with ``ROUND_HALF_UP`` (halves
away from zero). This reproduces the previous ``float`` + Go-``math.Round``
behaviour exactly while removing binary-float drift that would otherwise
accumulate through the multiplications/divisions in the forward analytics
(purchase planning, price/volume/mix bridges). ``Decimal`` values are serialized
back to JSON numbers at the MCP boundary (see ``Money`` in
:mod:`chic.aggregate.models`).

Non-money quantities (units, days) and percentages stay ``float`` and keep the
original half-away rounding via :func:`round2` — they are display values, not
amounts that feed further money arithmetic.
"""

from __future__ import annotations

import math
from datetime import datetime
from decimal import ROUND_HALF_UP, Decimal

_TIME_FORMAT = "%Y-%m-%d %H:%M:%S"
_CENTS = Decimal("0.01")
_ONE = Decimal("1")


def dec(x: float | int | str | Decimal) -> Decimal:
    """Coerce to ``Decimal`` via ``str`` so a float uses its shortest repr.

    ``Decimal(str(0.1))`` is ``Decimal('0.1')`` (what the source number *meant*),
    not the exact binary expansion ``Decimal(0.1)`` would give.
    """
    return x if isinstance(x, Decimal) else Decimal(str(x))


def money_round(v: Decimal) -> Decimal:
    """Quantize a money amount to 2 decimals, halves away from zero."""
    return v.quantize(_CENTS, rounding=ROUND_HALF_UP)


def minor_to_major(minor: float | Decimal) -> Decimal:
    """Minor units → major units (Decimal, 2 decimals).

    Fractional minor units (rare, but the API may return them) are rounded to a
    whole minor unit half-away first, matching the previous ``math.Round`` path.
    """
    whole = dec(minor).quantize(_ONE, rounding=ROUND_HALF_UP)
    return money_round(whole / 100)


def round_half_away(x: float) -> float:
    """Round to the nearest integer, halves away from zero (like Go math.Round)."""
    return math.floor(x + 0.5) if x >= 0 else math.ceil(x - 0.5)


def round2(v: float) -> float:
    """Round a non-money quantity/percentage to two decimals, halves away from zero."""
    return round_half_away(v * 100) / 100.0


def margin_pct(profit: float | Decimal, revenue: float | Decimal) -> float:
    r = float(revenue)
    if r == 0:
        return 0.0
    return round2(float(profit) / r * 100)


def pct_change(a: float | Decimal, b: float | Decimal) -> float:
    a = float(a)
    b = float(b)
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
