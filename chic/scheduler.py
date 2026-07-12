"""In-process job scheduler (APScheduler) with a SQLite job store.

One ``AsyncIOScheduler`` fires the app's jobs by cron in the app's own event loop —
no extra service. Job state is persisted to a dedicated SQLite file, so schedules
survive restarts and a fire missed while the app was briefly down still runs (within
a grace window). This is only the lifecycle wrapper; jobs live in :mod:`chic.jobs`.

Per the APScheduler guide, a persistent job store requires each job to have an
explicit ``id`` and ``replace_existing=True`` (so a restart updates, not duplicates),
and the target must be an importable module-level callable — hence jobs read their
deps from :func:`chic.jobs.set_context` rather than closing over them.
"""

from __future__ import annotations

import logging
from collections.abc import Iterable
from typing import Any

from apscheduler.events import EVENT_JOB_ERROR
from apscheduler.jobstores.sqlalchemy import SQLAlchemyJobStore
from apscheduler.schedulers.asyncio import AsyncIOScheduler
from apscheduler.triggers.cron import CronTrigger

from chic.jobs import ScheduledJob

logger = logging.getLogger(__name__)

_MISFIRE_GRACE_SECONDS = 3600  # still run a fire missed by up to an hour


class Scheduler:
    def __init__(self, *, jobs_db: str, timezone: str) -> None:
        self._tz = timezone
        self._wanted: set[str] = set()
        self._sched = AsyncIOScheduler(
            jobstores={"default": SQLAlchemyJobStore(url=f"sqlite:///{jobs_db}")},
            timezone=timezone,
        )
        self._sched.add_listener(self._on_error, EVENT_JOB_ERROR)

    def register(self, jobs: Iterable[ScheduledJob]) -> None:
        """Register (or update) each job by its cron. Code owns the schedule."""
        for job in jobs:
            self._sched.add_job(
                job.func,
                CronTrigger.from_crontab(job.cron, timezone=self._tz),
                id=job.id,
                replace_existing=True,  # a restart updates the job, never duplicates it
                misfire_grace_time=_MISFIRE_GRACE_SECONDS,
                coalesce=True,  # collapse a backlog of missed fires into one
                max_instances=1,  # never overlap a job with itself
            )
            self._wanted.add(job.id)
            logger.info("scheduled job %s @ %s (%s)", job.id, job.cron, self._tz)

    def job_ids(self) -> list[str]:
        return [j.id for j in self._sched.get_jobs()]

    def start(self) -> None:
        self._sched.start()
        self._reconcile()

    def _reconcile(self) -> None:
        """Drop persisted jobs the schedule no longer lists. The persistent store
        outlives the code, so a job removed or renamed in the schedule would keep
        firing from jobs.db on its old cron (or crash startup if its func is gone).
        Runs after ``start()`` because that is when the store's jobs are loaded."""
        for existing in self._sched.get_jobs():
            if existing.id not in self._wanted:
                self._sched.remove_job(existing.id)
                logger.info("removed stale job %s not in schedule", existing.id)

    def shutdown(self) -> None:
        self._sched.shutdown(wait=False)

    @staticmethod
    def _on_error(event: Any) -> None:
        logger.error("scheduled job %s raised", event.job_id, exc_info=event.exception)
