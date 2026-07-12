"""The schedule — the single, explicit place where jobs get a cron.

No defaults live anywhere else: a job runs **only** if it is listed here. To add a
job, put its module under ``chic/jobs/`` (with an ``async def run()``) and add one
``ScheduledJob`` line below. Times are 5-field cron, interpreted in ``TIMEZONE``.
"""

from __future__ import annotations

from chic.clock import TIMEZONE
from chic.jobs import digest, snapshot
from chic.jobs.base import ScheduledJob

__all__ = ["SCHEDULE", "TIMEZONE"]

SCHEDULE: list[ScheduledJob] = [
    ScheduledJob(id="snapshot", cron="0 3 * * *", func=snapshot.run),
    ScheduledJob(id="digest", cron="0 8 * * *", func=digest.run),
]
