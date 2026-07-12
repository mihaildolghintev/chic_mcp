"""Morning digest job: send the day's summary to the allowlisted recipients.

Fires once a day by cron, so it needs no dedupe — the schedule *is* the "once a day"
guarantee (unlike the old interval ticker). The text is composed by code, so every
figure is grounded by construction; money is formatted for the ru locale via Babel.
"""

from __future__ import annotations

import logging
from decimal import Decimal

from babel.numbers import format_currency, format_decimal

from chic import usecases
from chic.aggregate.models import DashboardSummary
from chic.aggregate.money import dec
from chic.jobs.base import get_context, get_currency

logger = logging.getLogger(__name__)

_LOCALE = "ru"


def _money(amount: float | Decimal, code: str) -> str:
    value = dec(amount)
    if code:
        return format_currency(value, code, locale=_LOCALE)
    return format_decimal(value, format="#,##0.00", locale=_LOCALE)


def compose(dash: DashboardSummary, code: str) -> str:
    """Build the digest text (HTML for Telegram) from a day's dashboard."""
    return (
        "☀️ <b>Сводка за день</b>\n\n"
        f"🛒 Продажи: {_money(dash.sales_amount, code)} ({dash.sales_count} шт.)\n"
        f"    к прошлому периоду: {_money(dash.sales_delta_vs_prev, code)}\n"
        f"📦 Заказы: {_money(dash.orders_amount, code)} ({dash.orders_count})\n"
        f"💰 Деньги: приход {_money(dash.money_income, code)}, "
        f"расход {_money(dash.money_outcome, code)}, "
        f"остаток {_money(dash.money_balance, code)}"
    )


async def run() -> None:
    ctx = get_context()
    if ctx is None or not ctx.recipients:
        logger.warning("digest job skipped: no recipients")
        return
    dash = await usecases.dashboard(ctx.api, "day")
    text = compose(dash, get_currency())
    for chat_id in ctx.recipients:
        # One bad recipient (blocked the bot, deactivated, stale id) must not rob
        # everyone after them of the digest — the cron only fires once a day.
        try:
            await ctx.notify(chat_id, text)
        except Exception:
            logger.warning("digest delivery to %s failed", chat_id, exc_info=True)
