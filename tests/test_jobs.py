from __future__ import annotations

from decimal import Decimal
from pathlib import Path
from typing import cast

from chic.cache import Source
from chic.history import HistoryStore
from chic.jobs import JobContext, digest, set_context, set_currency, snapshot
from chic.jobs.base import Notify
from chic.jobs.schedule import SCHEDULE, TIMEZONE
from chic.moysklad import ProfitOptions, StockOptions
from chic.moysklad.models import (
    Dashboard,
    DashboardCount,
    DashboardMoney,
    Meta,
    NamedRef,
    ProfitByProductRow,
    StockRow,
)
from chic.scheduler import Scheduler


class _FakeApi:
    async def get_stock(self, opts: StockOptions) -> list[StockRow]:
        return [StockRow(meta=Meta(href="h1"), name="Widget", stock=10.0, price=5000.0)]

    async def profit_by_product(
        self, variant: bool, opts: ProfitOptions
    ) -> list[ProfitByProductRow]:
        return [
            ProfitByProductRow(
                assortment=NamedRef(meta=Meta(href="h1"), name="Widget"),
                sell_quantity=6.0,
                sell_sum=60000.0,
                profit=24000.0,
            )
        ]

    async def get_dashboard(self, period: str) -> Dashboard:
        return Dashboard(
            sales=DashboardCount(count=3, amount=500000, movement_amount=10000),
            orders=DashboardCount(count=2, amount=200000),
            money=DashboardMoney(income=700000, outcome=100000, balance=600000),
        )


async def _noop(chat_id: int, text: str) -> None:
    pass


def _ctx(
    *, history: HistoryStore | None = None, notify: Notify = _noop, recipients: tuple[int, ...] = ()
) -> JobContext:
    return JobContext(
        api=cast(Source, _FakeApi()), history=history, notify=notify, recipients=recipients
    )


# ---- schedule (single explicit source) ------------------------------------


def test_schedule_lists_both_jobs_explicitly() -> None:
    by_id = {j.id: j for j in SCHEDULE}
    assert by_id["snapshot"].cron == "0 3 * * *"
    assert by_id["snapshot"].func is snapshot.run
    assert by_id["digest"].cron == "0 8 * * *"
    assert by_id["digest"].func is digest.run
    assert TIMEZONE == "Europe/Chisinau"


# ---- snapshot job ---------------------------------------------------------


async def test_snapshot_run_via_context(tmp_path: Path) -> None:
    from chic import clock

    history = await HistoryStore.open(str(tmp_path / "history.db"))
    try:
        set_context(_ctx(history=history))
        await snapshot.run()
        # Snapshot dates the row by the account-local wall clock, not container time.
        assert await history.has_stock(clock.now().strftime("%Y-%m-%d"))
    finally:
        set_context(_ctx())  # reset global
        await history.close()


async def test_snapshot_run_no_history_is_noop() -> None:
    set_context(_ctx(history=None))
    await snapshot.run()  # must not raise when history is disabled


# ---- digest job -----------------------------------------------------------


def test_digest_compose_has_grounded_figures() -> None:
    from chic.aggregate.models import DashboardSummary

    dash = DashboardSummary(
        period="day",
        sales_count=3,
        sales_amount=Decimal("5000"),
        sales_delta_vs_prev=Decimal("100"),
        orders_count=2,
        orders_amount=Decimal("2000"),
        money_income=Decimal("7000"),
        money_outcome=Decimal("1000"),
        money_balance=Decimal("6000"),
    )
    text = digest.compose(dash, "MDL")
    assert "Сводка за день" in text
    assert "MDL" in text  # Babel appends the account currency
    assert "(3 шт.)" in text


async def test_digest_run_sends_to_each_recipient() -> None:
    sent: list[tuple[int, str]] = []

    async def notify(chat_id: int, text: str) -> None:
        sent.append((chat_id, text))

    set_currency("MDL")
    set_context(_ctx(notify=notify, recipients=(111, 222)))
    try:
        await digest.run()
        assert [cid for cid, _ in sent] == [111, 222]
        assert "Сводка за день" in sent[0][1]
    finally:
        set_currency("")
        set_context(_ctx())


async def test_digest_run_no_recipients_is_noop() -> None:
    set_context(_ctx(recipients=()))
    await digest.run()  # must not raise / send when nobody is configured


async def test_digest_run_continues_past_failing_recipient() -> None:
    sent: list[int] = []

    async def notify(chat_id: int, text: str) -> None:
        if chat_id == 111:  # e.g. this user blocked the bot
            raise RuntimeError("Forbidden: bot was blocked by the user")
        sent.append(chat_id)

    set_currency("MDL")
    set_context(_ctx(notify=notify, recipients=(111, 222, 333)))
    try:
        await digest.run()  # must not raise despite 111 failing
        assert sent == [222, 333]  # everyone after the failure still gets it
    finally:
        set_currency("")
        set_context(_ctx())


# ---- scheduler + SQLite job store -----------------------------------------


async def test_scheduler_registers_schedule_in_sqlite_store(tmp_path: Path) -> None:
    scheduler = Scheduler(jobs_db=str(tmp_path / "jobs.db"), timezone=TIMEZONE)
    scheduler.register(SCHEDULE)
    scheduler.start()
    try:
        assert set(scheduler.job_ids()) == {"snapshot", "digest"}
    finally:
        scheduler.shutdown()
    assert (tmp_path / "jobs.db").exists()


async def test_scheduler_prunes_jobs_dropped_from_schedule(tmp_path: Path) -> None:
    from chic.jobs.base import ScheduledJob

    jobs_db = str(tmp_path / "jobs.db")
    # A previous release scheduled an extra job, now persisted in jobs.db.
    old = Scheduler(jobs_db=jobs_db, timezone=TIMEZONE)
    old.register([*SCHEDULE, ScheduledJob(id="legacy", cron="0 5 * * *", func=snapshot.run)])
    old.start()  # start() flushes the jobs to the persistent store
    assert "legacy" in old.job_ids()
    old.shutdown()

    # The current schedule no longer lists it: registering must prune the stale row.
    fresh = Scheduler(jobs_db=jobs_db, timezone=TIMEZONE)
    fresh.register(SCHEDULE)
    fresh.start()
    try:
        assert set(fresh.job_ids()) == {"snapshot", "digest"}  # "legacy" is gone
    finally:
        fresh.shutdown()
