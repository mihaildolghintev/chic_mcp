"""The single source of "now" for the whole app.

MoySklad stores every timestamp *naive*, in the account's own timezone, and the
scheduler fires cron in that same zone. So every "now" the app computes must be
that zone's wall clock — never the container's local time (which is UTC in prod).
Returning a *naive* datetime keeps it drop-in compatible with the naive MoySklad
timestamps that :func:`chic.aggregate.money.parse_time` yields (subtracting a
tz-aware now from a naive moment would raise), while still being anchored to the
account timezone regardless of where the process runs.
"""

from __future__ import annotations

from datetime import datetime
from zoneinfo import ZoneInfo

# The account's wall-clock timezone. Shared by the scheduler (cron) and every
# "now" below so day boundaries never disagree.
TIMEZONE = "Europe/Chisinau"
_TZ = ZoneInfo(TIMEZONE)


def now() -> datetime:
    """Account-local wall clock as a naive datetime (matches MoySklad timestamps)."""
    return datetime.now(_TZ).replace(tzinfo=None)
