"""The shared truncating-report envelope helper.

Every ``*_report`` caps its detail rows to a display limit while its ``totals``
still cover the full set. ``truncate`` is that one primitive, used across the
aggregate modules — public because it is genuinely shared, not module-private.
"""

from __future__ import annotations


def truncate[Row](rows: list[Row], limit: int) -> tuple[list[Row], int, bool]:
    """Cap rows to a display limit (<=0 ⇒ all); report whether truncation happened."""
    full = len(rows)
    if 0 < limit < full:
        return rows[:limit], full, True
    return rows, full, False
