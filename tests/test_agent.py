from __future__ import annotations

from collections.abc import AsyncIterator
from pathlib import Path

import pytest_asyncio
from chic.agent import ChicAgent
from chic.agent.agent import _clarify_result, _parse_consolidated, _RateLimiter, _tool_outputs
from chic.store import Store
from mcp.server.fastmcp import FastMCP
from pydantic_ai.messages import (
    ModelMessage,
    ModelRequest,
    ModelResponse,
    TextPart,
    ToolCallPart,
    ToolReturnPart,
)
from pydantic_ai.models.function import AgentInfo, FunctionModel

UID = 7


@pytest_asyncio.fixture
async def agent(tmp_path: Path) -> AsyncIterator[ChicAgent]:
    store = await Store.open(str(tmp_path / "app.db"))
    a = await ChicAgent.create(
        fastmcp=FastMCP("test"),  # no MoySklad tools needed for these flows
        store=store,
        deepseek_api_key="test",
        deepseek_model="test",
        deepseek_base_url="http://localhost",
    )
    try:
        yield a
    finally:
        await store.close()


# ---- pure units -----------------------------------------------------------


def test_rate_limiter_allows_then_blocks() -> None:
    rl = _RateLimiter(per_hour=2)
    assert rl.allow(UID, 1000.0) is True
    assert rl.allow(UID, 1000.0) is True
    assert rl.allow(UID, 1000.0) is False  # bucket empty
    assert rl.allow(UID, 1000.0 + 3600) is True  # refilled after an hour


def test_rate_limiter_unlimited() -> None:
    rl = _RateLimiter(per_hour=-1)
    assert all(rl.allow(UID, 1000.0) for _ in range(100))


def test_clarify_result_degrades_below_two_options() -> None:
    r = _clarify_result("Вопрос?", ["один"], False)
    assert r.text == "Вопрос?"
    assert r.options == []


def test_clarify_result_keeps_options() -> None:
    r = _clarify_result("Период?", ["день", "неделя"], True)
    assert r.options == ["день", "неделя"]
    assert r.allow_custom is True


def test_parse_consolidated_enforces_bounds() -> None:
    raw = 'prefix {"preferences":[{"key":"lang","value":"ru"},{"key":"","value":"x"}]} suffix'
    prefs = _parse_consolidated(raw)
    assert [(p.key, p.value) for p in prefs] == [("lang", "ru")]  # empty key dropped


# ---- integration via FunctionModel ---------------------------------------


async def test_plain_answer_is_persisted(agent: ChicAgent) -> None:
    def reply(_messages: list[ModelMessage], _info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart(content="Привет!")])

    with agent._agent.override(model=FunctionModel(reply)):
        result = await agent.handle(UID, "здравствуй")

    assert result.text == "Привет!"
    assert result.options == []
    # Exchange persisted to history.
    msgs = await agent._store.recent_messages(UID, 10)
    assert [(m.role, m.content) for m in msgs] == [
        ("user", "здравствуй"),
        ("assistant", "Привет!"),
    ]


def test_tool_outputs_collects_tool_return_strings() -> None:
    messages: list[ModelMessage] = [
        ModelRequest(
            parts=[
                ToolReturnPart(
                    tool_name="get_dashboard", content='{"moneyBalance": 6000.0}', tool_call_id="c1"
                )
            ]
        ),
        ModelResponse(parts=[TextPart(content="Баланс 6 000")]),
    ]
    assert _tool_outputs(messages) == ['{"moneyBalance": 6000.0}']


async def test_grounding_does_not_alter_or_block_answer(agent: ChicAgent) -> None:
    # Even a fabricated figure passes through untouched — grounding only measures.
    def reply(_messages: list[ModelMessage], _info: AgentInfo) -> ModelResponse:
        return ModelResponse(parts=[TextPart(content="Баланс 999 999 999 MDL")])

    with agent._agent.override(model=FunctionModel(reply)):
        result = await agent.handle(UID, "какой баланс?")

    assert result.text == "Баланс 999 999 999 MDL"


async def test_clarify_output_returns_a_clarifying_question(agent: ChicAgent) -> None:
    def ask(_messages: list[ModelMessage], info: AgentInfo) -> ModelResponse:
        # An ambiguous request finalises via the built-in output tool (final_result),
        # not a bespoke ask_user tool — a successful terminal result, not an error.
        return ModelResponse(
            parts=[
                ToolCallPart(
                    tool_name=info.output_tools[0].name,
                    args={"question": "Какой период?", "options": ["день", "неделя"]},
                )
            ]
        )

    with agent._agent.override(model=FunctionModel(ask)):
        result = await agent.handle(UID, "покажи продажи")

    assert result.text == "Какой период?"
    assert result.options == ["день", "неделя"]


async def test_remember_preference_writes_to_store(agent: ChicAgent) -> None:
    calls = {"n": 0}

    def script(_messages: list[ModelMessage], _info: AgentInfo) -> ModelResponse:
        calls["n"] += 1
        if calls["n"] == 1:
            return ModelResponse(
                parts=[
                    ToolCallPart(
                        tool_name="remember_preference",
                        args={"key": "language", "value": "английский"},
                    )
                ]
            )
        return ModelResponse(parts=[TextPart(content="ок")])

    with agent._agent.override(model=FunctionModel(script)):
        await agent.handle(UID, "отвечай по-английски")

    prefs = {p.key: p.value for p in await agent._store.preferences(UID)}
    assert prefs == {"language": "английский"}
