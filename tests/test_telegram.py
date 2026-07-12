from __future__ import annotations

from collections.abc import AsyncIterator
from pathlib import Path

import pytest_asyncio
from chic.agent import ChicAgent
from chic.store import Store
from chic.telegram import ChicBot
from chic.telegram.dedupe import Dedupe
from chic.telegram.keyboards import (
    CLARIFY_CUSTOM,
    button_label,
    clarify_keyboard,
    feedback_keyboard,
    index_from_callback,
    menu_keyboard,
    parse_feedback,
    render_memory,
    template_by_callback,
)
from chic.telegram.render import source_chunks, to_markdown_v2
from mcp.server.fastmcp import FastMCP

FAKE_TOKEN = "123456:AAHfakefakefakefakefakefakefakefake"


# ---- dedupe ---------------------------------------------------------------


def test_dedupe_first_seen() -> None:
    d = Dedupe(4)
    assert d.first_seen(10) is True
    assert d.first_seen(10) is False
    assert d.first_seen(11) is True


# ---- keyboards / callback scheme ------------------------------------------


def test_index_from_callback() -> None:
    assert index_from_callback("ask:3", "ask:") == 3
    assert index_from_callback("mf:0", "mf:") == 0
    assert index_from_callback("ask:x", "ask:") is None
    assert index_from_callback("other", "ask:") is None


def test_template_by_callback() -> None:
    question = template_by_callback("ask:0")
    assert question is not None
    assert question.startswith("Прибыльность за последние 7 дней")
    assert template_by_callback("ask:999") is None


def test_menu_keyboard_two_buttons_per_row() -> None:
    from chic.telegram.keyboards import QUESTION_TEMPLATES

    kb = menu_keyboard()
    flat = [btn for row in kb.inline_keyboard for btn in row]
    assert len(flat) == len(QUESTION_TEMPLATES)
    assert all(len(row) <= 2 for row in kb.inline_keyboard)
    assert kb.inline_keyboard[0][0].callback_data == "ask:0"


def test_parse_feedback() -> None:
    assert parse_feedback("fb:l:abc123") == ("like", "abc123")
    assert parse_feedback("fb:d:def456") == ("dislike", "def456")
    assert parse_feedback("fb:l:") is None  # no span id
    assert parse_feedback("other") is None


def test_feedback_keyboard_hidden_without_span() -> None:
    assert feedback_keyboard("") is None
    kb = feedback_keyboard("span123")
    assert kb is not None
    assert kb.inline_keyboard[0][0].callback_data == "fb:l:span123"


def test_clarify_keyboard_and_button_label() -> None:
    kb = clarify_keyboard(["день", "неделя"], allow_custom=True)
    assert kb.inline_keyboard[0][0].callback_data == "cq:0"
    assert kb.inline_keyboard[-1][0].callback_data == CLARIFY_CUSTOM
    assert button_label(kb, "cq:1") == "неделя"
    assert button_label(kb, "cq:99") == ""


def test_render_memory_empty_and_items() -> None:
    text, markup = render_memory([])
    assert markup is None
    assert "Пока я не запомнил" in text

    text, markup = render_memory([("language", "ru"), ("style", "short")])
    assert markup is not None
    assert "<b>language</b>: ru" in text
    assert markup.inline_keyboard[0][0].callback_data == "mf:0"


def test_render_memory_escapes_html() -> None:
    text, _ = render_memory([("k", "<script>")])
    assert "&lt;script&gt;" in text


# ---- rendering ------------------------------------------------------------


def test_source_chunks_single_when_short() -> None:
    assert source_chunks("привет") == ["привет"]


def test_source_chunks_splits_long_text() -> None:
    text = "\n".join(f"строка {i}" for i in range(2000))
    chunks = source_chunks(text)
    assert len(chunks) > 1
    assert all(len(c) <= 3500 for c in chunks)


def test_to_markdown_v2_escapes() -> None:
    out = to_markdown_v2("**Итог:** 12 345 MDL")
    assert "Итог" in out
    assert "*" in out  # bold marker present


# ---- bot construction -----------------------------------------------------


@pytest_asyncio.fixture
async def bot(tmp_path: Path) -> AsyncIterator[ChicBot]:
    store = await Store.open(str(tmp_path / "app.db"))
    agent = await ChicAgent.create(
        fastmcp=FastMCP("test"),
        store=store,
        deepseek_api_key="test",
        deepseek_model="test",
        deepseek_base_url="http://localhost",
    )
    b = ChicBot(token=FAKE_TOKEN, webhook_secret="secret", allowed_ids={1, 2}, agent=agent)
    try:
        yield b
    finally:
        await b.close()
        await store.close()


async def test_bot_builds(bot: ChicBot) -> None:
    assert bot.webhook_secret == "secret"
