"""Inline keyboards + callback-data scheme.

callback_data is capped at 64 bytes, so option/preference buttons carry only an
index; the label is recovered from the pressed message. Feedback buttons carry
the trace span id (16 hex chars).
"""

from __future__ import annotations

from html import escape

from aiogram.types import InlineKeyboardButton, InlineKeyboardMarkup

QUICK_PREFIX = "ask:"
MEM_FORGET_PREFIX = "mf:"
CLARIFY_PREFIX = "cq:"
CLARIFY_CUSTOM = "cq:custom"
FEEDBACK_PREFIX = "fb:"
FEEDBACK_LIKE = "fb:l:"
FEEDBACK_DISLIKE = "fb:d:"

RATING_LIKE = "like"
RATING_DISLIKE = "dislike"

MSG_MENU_PROMPT = "Выберите готовый вопрос или напишите свой:"
MSG_MEMORY_EMPTY = (
    "🧠 Пока я не запомнил никаких ваших предпочтений.\n\n"
    "Когда вы зададите устойчивое пожелание (язык общения, формат ответов, "
    "основной склад…), я сохраню его и покажу здесь."
)
MSG_MEMORY_HEADER = "🧠 <b>Что я о вас помню</b>\n\nНажмите 🗑, чтобы удалить пункт."
MSG_ASK_CUSTOM = "Напишите свой вариант ответом 👇"
MSG_FEEDBACK_THANKS = "Спасибо за оценку 🙏"

# (button label, question sent to the agent when tapped)
# Ordered owner-first (money & profit on top), then sales, then warehouse.
# Rendered two-per-row; keep pairs adjacent so the grid groups by block.
QUESTION_TEMPLATES: list[tuple[str, str]] = [
    # --- деньги и прибыль ---
    (
        "💰 Прибыль за неделю",
        "Прибыльность за последние 7 дней. Возьми итоги (totals) за весь "
        "период и покажи оборот (сумму продаж), себестоимость, выручку, "
        "прибыль и маржу в %. Ниже — 3-5 самых прибыльных товаров.",
    ),
    (
        "📈 Итоги месяца",
        "Сводка за текущий месяц: сумма продаж, количество заказов, деньги "
        "(приход, расход, остаток) и насколько каждый показатель изменился "
        "к прошлому месяцу. Кратко, главными цифрами.",
    ),
    (
        "📉 Месяц vs прошлый",
        "Сравни этот месяц с прошлым по выручке и прибыли: итоги за оба "
        "периода, разницу в деньгах и в %, и топ товаров — драйверов роста "
        "и падения.",
    ),
    (
        "💵 Деньги за сегодня",
        "Движение денег за сегодня: сколько поступило, сколько ушло и чистый "
        "итог. Если есть разбивка по счетам или кассам — покажи главное.",
    ),
    # --- заявки и продажи ---
    (
        "📊 Заявки за сегодня",
        "Заявки (заказы покупателей) за сегодня. Сколько заказов оформлено "
        "и на какую сумму — дай итоги за сегодня; если заказов немного, "
        "перечисли их коротко.",
    ),
    (
        "🛍 Продажи за неделю",
        "Продажи (отгрузки) за последние 7 дней: сумма и количество за "
        "период и динамика по дням. Дай итог и короткий вывод по тренду.",
    ),
    # --- склад ---
    (
        "💎 Деньги в товаре",
        "Сколько денег заморожено в товаре: суммарная стоимость остатков по "
        "себестоимости, сколько всего позиций и единиц. Если можно — "
        "разбивка по складам.",
    ),
    (
        "🔄 Оборачиваемость",
        "Оборачиваемость товара за последний месяц: что продаётся быстро, "
        "а что залёживается (дни оборота). Покажи 5 самых оборачиваемых "
        "и 5 самых медленных.",
    ),
]


def index_from_callback(data: str, prefix: str) -> int | None:
    if not data.startswith(prefix):
        return None
    try:
        idx = int(data[len(prefix) :])
    except ValueError:
        return None
    return idx if idx >= 0 else None


def menu_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        InlineKeyboardButton(text=label, callback_data=f"{QUICK_PREFIX}{i}")
        for i, (label, _) in enumerate(QUESTION_TEMPLATES)
    ]
    # Two per row: compact grid; templates are ordered so each pair is one block.
    rows = [buttons[i : i + 2] for i in range(0, len(buttons), 2)]
    return InlineKeyboardMarkup(inline_keyboard=rows)


def template_by_callback(data: str) -> str | None:
    idx = index_from_callback(data, QUICK_PREFIX)
    if idx is None or idx >= len(QUESTION_TEMPLATES):
        return None
    return QUESTION_TEMPLATES[idx][1]


def render_memory(items: list[tuple[str, str]]) -> tuple[str, InlineKeyboardMarkup | None]:
    if not items:
        return MSG_MEMORY_EMPTY, None
    lines = [MSG_MEMORY_HEADER, ""]
    rows: list[list[InlineKeyboardButton]] = []
    for i, (key, value) in enumerate(items):
        lines.append(f"• <b>{escape(key)}</b>: {escape(value)}")
        rows.append(
            [InlineKeyboardButton(text=f"🗑 {key}", callback_data=f"{MEM_FORGET_PREFIX}{i}")]
        )
    return "\n".join(lines), InlineKeyboardMarkup(inline_keyboard=rows)


def clarify_keyboard(options: list[str], allow_custom: bool) -> InlineKeyboardMarkup:
    rows = [
        [InlineKeyboardButton(text=opt, callback_data=f"{CLARIFY_PREFIX}{i}")]
        for i, opt in enumerate(options)
    ]
    if allow_custom:
        rows.append([InlineKeyboardButton(text="✏️ Свой вариант", callback_data=CLARIFY_CUSTOM)])
    return InlineKeyboardMarkup(inline_keyboard=rows)


def feedback_keyboard(span_id: str) -> InlineKeyboardMarkup | None:
    if not span_id:
        return None
    return InlineKeyboardMarkup(
        inline_keyboard=[
            [
                InlineKeyboardButton(text="👍", callback_data=f"{FEEDBACK_LIKE}{span_id}"),
                InlineKeyboardButton(text="👎", callback_data=f"{FEEDBACK_DISLIKE}{span_id}"),
            ]
        ]
    )


def parse_feedback(data: str) -> tuple[str, str] | None:
    """Return (rating, span_id) or None if not a feedback press / no span id."""
    if data.startswith(FEEDBACK_LIKE):
        span = data[len(FEEDBACK_LIKE) :]
        return (RATING_LIKE, span) if span else None
    if data.startswith(FEEDBACK_DISLIKE):
        span = data[len(FEEDBACK_DISLIKE) :]
        return (RATING_DISLIKE, span) if span else None
    return None


def button_label(markup: InlineKeyboardMarkup | None, data: str) -> str:
    """Recover a button's text by its callback_data (labels aren't in callback data)."""
    if markup is None:
        return ""
    for row in markup.inline_keyboard:
        for btn in row:
            if btn.callback_data == data:
                return btn.text
    return ""
