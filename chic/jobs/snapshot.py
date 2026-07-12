"""Daily snapshot job: capture stock + sales into history.db.

Idempotent and self-gating — safe to fire repeatedly, and no-ops when snapshot
history isn't configured. On a fresh deploy it seeds the last ``MAX_BACKFILL_DAYS``
days of sales, and after an outage it backfills the missed days in one pass.
"""

from __future__ import annotations

import logging

from chic.history import SnapshotService
from chic.jobs.base import get_context

logger = logging.getLogger(__name__)

# How many past sales-days one run may backfill: caps outage catch-up and doubles as
# the initial seed window on a fresh deploy. Matches purchase_plan's fallback window.
MAX_BACKFILL_DAYS = 30


async def run() -> None:
    ctx = get_context()
    if ctx is None or ctx.history is None:
        logger.warning("snapshot job skipped: history is not configured")
        return
    await SnapshotService(ctx.api, ctx.history).capture(max_backfill_days=MAX_BACKFILL_DAYS)
