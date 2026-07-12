"""Shared plumbing for scheduled jobs: the run context and the schedule-entry type.

Each job is a module-level ``async def run()`` under this package (one job per file).
The SQLite job store references those functions by import path, so they can't close
over live objects — a job reads its dependencies from the context set once at startup.
The account currency is resolved asynchronously after startup, so it lives separately
and is updated via :func:`set_currency`.
"""

from __future__ import annotations

import dataclasses
from collections.abc import Awaitable, Callable

from chic.cache import Source
from chic.history import HistoryStore

# Deliver one text message to one chat (e.g. the bot's send).
Notify = Callable[[int, str], Awaitable[None]]


@dataclasses.dataclass(frozen=True)
class JobContext:
    """Live dependencies the jobs run against (wired once at startup)."""

    api: Source
    history: HistoryStore | None
    notify: Notify
    recipients: tuple[int, ...]


@dataclasses.dataclass(frozen=True)
class ScheduledJob:
    """One schedule entry: a job's id, its cron, and the coroutine to run."""

    id: str
    cron: str
    func: Callable[[], Awaitable[None]]


_context: JobContext | None = None
_currency: str = ""


def set_context(context: JobContext) -> None:
    global _context
    _context = context


def get_context() -> JobContext | None:
    return _context


def set_currency(code: str) -> None:
    """Set the account currency (resolved in the background after startup)."""
    global _currency
    _currency = code


def get_currency() -> str:
    return _currency
