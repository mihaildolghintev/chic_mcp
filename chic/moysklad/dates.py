"""Date/period normalization for MoySklad ``moment*`` parameters.

MoySklad expects ``YYYY-MM-DD HH:MM:SS``. A bare date gets midnight; for the
inclusive end of a range it gets the last second of the day (``momentTo`` is
inclusive of the instant).
"""

from __future__ import annotations


def normalize_moment(s: str) -> str:
    s = s.strip()
    if not s:
        return ""
    # Already has a time component (space-separated).
    if " " in s:
        return s
    if "T" in s:
        date_part, _, rest = s.partition("T")
        time_part = rest.removesuffix("Z")
        if len(time_part) >= 8:
            time_part = time_part[:8]
        return f"{date_part} {time_part}"
    return f"{s} 00:00:00"


def normalize_moment_end(s: str) -> str:
    s = s.strip()
    if not s:
        return ""
    if " " in s or "T" in s:
        return normalize_moment(s)
    return f"{s} 23:59:59"
