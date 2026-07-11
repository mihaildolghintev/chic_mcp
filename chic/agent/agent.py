"""The bot's brain: a pydantic-ai agent over the MoySklad MCP tools.

Faithful port of the Go agent. The MoySklad tools are forwarded to the
in-process FastMCP server; ``ask_user`` is a virtual tool intercepted as a
terminal turn (the human's choice returns as the next message); rolling summary
and preference consolidation are hand-rolled (rune budgets + injection-hardening
that no framework owns). Token stop-loss and round cap use pydantic-ai's
``UsageLimits``.
"""

from __future__ import annotations

import dataclasses
import json
import logging
import time
from datetime import datetime
from typing import Any, cast

from mcp.server.fastmcp import FastMCP
from pydantic_ai import Agent, ImageUrl, RunContext
from pydantic_ai.exceptions import UsageLimitExceeded
from pydantic_ai.messages import ModelMessage, ModelRequest, ModelResponse, TextPart, UserPromptPart
from pydantic_ai.models.openai import OpenAIChatModel
from pydantic_ai.providers.openai import OpenAIProvider
from pydantic_ai.settings import ModelSettings
from pydantic_ai.tools import Tool
from pydantic_ai.usage import UsageLimits

from chic.agent.prompts import (
    CONSOLIDATE_SYSTEM_PROMPT,
    SUMMARY_SYSTEM_PROMPT,
    summary_context_block,
    system_prompt,
)
from chic.store import Message, Preference, Store

logger = logging.getLogger(__name__)

# Defaults (Go parity).
MAX_ROUNDS = 50
MAX_TOKENS = 1_000_000
HISTORY_DEPTH = 20
RATE_PER_HOUR = 30
SUMMARY_CHAR_BUDGET = 300_000
SUMMARY_KEEP_RECENT = 6
SUMMARY_MAX_CHARS = 1200
MAX_UNSUMMARIZED_SCAN = 400
MAX_TOOL_RESULT_CHARS = 40_000
MAX_PREF_KEY_LEN = 64
MAX_PREF_VALUE_LEN = 200
CONSOLIDATE_THRESHOLD = 8

MSG_RATE_LIMITED = "Слишком много запросов — сделайте паузу и попробуйте позже."
MSG_BUDGET_SPENT = "Запрос вышел слишком дорогим, я остановил обработку. Попробуйте сузить вопрос."
MSG_NO_VISION = "Обработка фото не настроена (нет vision-провайдера). Пришлите вопрос текстом."


@dataclasses.dataclass
class Result:
    """One agent turn: a plain answer, or a clarifying question with options."""

    text: str
    options: list[str] = dataclasses.field(default_factory=list)
    allow_custom: bool = False


@dataclasses.dataclass
class _Deps:
    user_id: int
    store: Store
    currency_code: str
    currency_name: str
    prefs: list[Preference]
    summary: str
    now: datetime


def _parse_ask_user(question: str, options: list[str], allow_custom: bool) -> Result:
    q = question.strip()
    opts = [o.strip() for o in options if o.strip()]
    if len(opts) < 2:  # not a real choice → plain-text question
        return Result(text=q)
    return Result(text=q, options=opts, allow_custom=allow_custom)


class _AskUser(Exception):
    """Raised by the ask_user tool to end the run and bounce a question to the human."""

    def __init__(self, question: str, options: list[str], allow_custom: bool) -> None:
        self.result = _parse_ask_user(question, options, allow_custom)
        super().__init__(question)


def _collapse(s: str) -> str:
    """Collapse whitespace (defends against a newline smuggling a fake prompt line)."""
    return " ".join(s.split())


def _truncate(s: str, n: int) -> str:
    return s if len(s) <= n else s[:n] + "\n…[результат обрезан]"


class _RateLimiter:
    """Per-user hourly token bucket (non-blocking allow(), like Go's rate.Allow())."""

    def __init__(self, per_hour: int) -> None:
        self._per_hour = per_hour
        self._buckets: dict[int, tuple[float, float]] = {}  # user_id → (tokens, last_ts)

    def allow(self, user_id: int, now_ts: float) -> bool:
        if self._per_hour < 0:
            return True
        cap = float(self._per_hour)
        refill = self._per_hour / 3600.0
        tokens, last = self._buckets.get(user_id, (cap, now_ts))
        tokens = min(cap, tokens + (now_ts - last) * refill)
        if tokens < 1.0:
            self._buckets[user_id] = (tokens, now_ts)
            return False
        self._buckets[user_id] = (tokens - 1.0, now_ts)
        return True


def _to_message_history(history: list[Message]) -> list[ModelMessage]:
    msgs: list[ModelMessage] = []
    for m in history:
        if m.role == "user":
            msgs.append(ModelRequest(parts=[UserPromptPart(content=m.content)]))
        elif m.role == "assistant":
            msgs.append(ModelResponse(parts=[TextPart(content=m.content)]))
    return msgs


def _parse_consolidated(raw: str) -> list[Preference]:
    s = raw.strip()
    start = s.find("{")
    end = s.rfind("}")
    if start < 0 or end <= start:
        return []
    try:
        parsed = json.loads(s[start : end + 1])
    except json.JSONDecodeError:
        return []
    out: list[Preference] = []
    seen: set[str] = set()
    for e in parsed.get("preferences", []) if isinstance(parsed, dict) else []:
        key = _collapse(str(e.get("key", "")))
        value = _collapse(str(e.get("value", "")))
        if not key or not value:
            continue
        if len(key) > MAX_PREF_KEY_LEN or len(value) > MAX_PREF_VALUE_LEN:
            continue
        if key in seen:
            continue
        seen.add(key)
        out.append(Preference(key=key, value=value))
    return out


class ChicAgent:
    """Answers one user message per :meth:`handle` call."""

    def __init__(
        self,
        *,
        store: Store,
        text_model: OpenAIChatModel,
        vision_model: OpenAIChatModel | None,
        agent: Agent[_Deps, str],
        summary_agent: Agent[None, str],
        consolidate_agent: Agent[None, str],
        currency_code: str,
        currency_name: str,
        summary_char_budget: int,
        rate_per_hour: int,
        max_rounds: int,
        max_tokens: int,
        history_depth: int,
    ) -> None:
        self._store = store
        self._text_model = text_model
        self._vision_model = vision_model
        self._agent = agent
        self._summary_agent = summary_agent
        self._consolidate_agent = consolidate_agent
        self._currency_code = currency_code
        self._currency_name = currency_name
        self._summary_char_budget = summary_char_budget
        self._history_depth = history_depth
        self._max_rounds = max_rounds
        self._max_tokens = max_tokens
        self._rate = _RateLimiter(rate_per_hour)

    @property
    def has_vision(self) -> bool:
        return self._vision_model is not None

    def set_currency(self, code: str, name: str) -> None:
        """Update the account currency labels (resolved lazily after startup)."""
        self._currency_code = code
        self._currency_name = name

    @classmethod
    async def create(
        cls,
        *,
        fastmcp: FastMCP,
        store: Store,
        deepseek_api_key: str,
        deepseek_model: str,
        deepseek_base_url: str,
        openai_api_key: str = "",
        openai_model: str = "",
        openai_base_url: str = "",
        currency_code: str = "",
        currency_name: str = "",
        summary_char_budget: int = SUMMARY_CHAR_BUDGET,
        rate_per_hour: int = RATE_PER_HOUR,
        max_rounds: int = MAX_ROUNDS,
        max_tokens: int = MAX_TOKENS,
        history_depth: int = HISTORY_DEPTH,
    ) -> ChicAgent:
        text_model = OpenAIChatModel(
            deepseek_model,
            provider=OpenAIProvider(base_url=deepseek_base_url, api_key=deepseek_api_key),
        )
        vision_model: OpenAIChatModel | None = None
        if openai_api_key.strip():
            vision_model = OpenAIChatModel(
                openai_model,
                provider=OpenAIProvider(base_url=openai_base_url, api_key=openai_api_key),
            )

        tools = await _build_moysklad_tools(fastmcp)
        agent: Agent[_Deps, str] = Agent(text_model, deps_type=_Deps, tools=tools)

        summary_agent: Agent[None, str] = Agent(text_model, instructions=SUMMARY_SYSTEM_PROMPT)
        consolidate_agent: Agent[None, str] = Agent(
            text_model, instructions=CONSOLIDATE_SYSTEM_PROMPT
        )

        self = cls(
            store=store,
            text_model=text_model,
            vision_model=vision_model,
            agent=agent,
            summary_agent=summary_agent,
            consolidate_agent=consolidate_agent,
            currency_code=currency_code,
            currency_name=currency_name,
            summary_char_budget=summary_char_budget,
            rate_per_hour=rate_per_hour,
            max_rounds=max_rounds,
            max_tokens=max_tokens,
            history_depth=history_depth,
        )

        @agent.instructions
        def _system(ctx: RunContext[_Deps]) -> str:
            return system_prompt(
                ctx.deps.now, ctx.deps.currency_code, ctx.deps.currency_name, ctx.deps.prefs
            )

        @agent.instructions
        def _summary(ctx: RunContext[_Deps]) -> str:
            return summary_context_block(ctx.deps.summary) if ctx.deps.summary else ""

        @agent.tool
        async def remember_preference(ctx: RunContext[_Deps], key: str, value: str) -> str:
            """Сохранить устойчивое предпочтение пользователя (язык общения, стиль ответов,
            специфика бизнеса). Повторный вызов с тем же key перезаписывает значение.
            Не для разовых вопросов и не для данных из отчётов."""
            return await self._remember(ctx.deps, key, value)

        @agent.tool
        async def forget_preference(ctx: RunContext[_Deps], key: str) -> str:
            """Удалить ранее сохранённое предпочтение пользователя по его key."""
            key = _collapse(key)
            if not key:
                return "ERROR: key обязателен и не может быть пустым"
            await ctx.deps.store.delete_preference(ctx.deps.user_id, key)
            return f"OK: предпочтение {key} удалено"

        @agent.tool_plain
        async def ask_user(question: str, options: list[str], allow_custom: bool = False) -> str:
            """Задать пользователю уточняющий вопрос с 2–4 взаимоисключающими вариантами,
            когда запрос неоднозначный (неясен период, склад, единица и т.п.). Обработка
            останавливается до ответа пользователя."""
            if not question.strip():
                return "ERROR: ask_user требует непустой question и минимум 2 варианта в options"
            raise _AskUser(question, options, allow_custom)

        return self

    # ---- public API -------------------------------------------------------

    async def reset(self, user_id: int) -> None:
        await self._store.start_session(user_id)

    async def preferences(self, user_id: int) -> list[Preference]:
        return await self._store.preferences(user_id)

    async def forget_preference(self, user_id: int, key: str) -> None:
        await self._store.delete_preference(user_id, key)

    async def handle(self, user_id: int, text: str, image_data_uri: str = "") -> Result:
        if not self._rate.allow(user_id, time.time()):
            return Result(text=MSG_RATE_LIMITED)
        if image_data_uri and not self.has_vision:
            return Result(text=MSG_NO_VISION)

        epoch = await self._store.session_epoch(user_id)
        prefs = await self._store.preferences(user_id)
        summary, history = await self._condense_history(user_id, epoch)
        deps = _Deps(
            user_id=user_id,
            store=self._store,
            currency_code=self._currency_code,
            currency_name=self._currency_name,
            prefs=prefs,
            summary=summary,
            now=datetime.now(),
        )

        stored = text
        user_prompt: str | list[str | ImageUrl]
        model: OpenAIChatModel
        if image_data_uri:
            parts: list[str | ImageUrl] = []
            if text:
                parts.append(text)
            parts.append(ImageUrl(url=image_data_uri))
            user_prompt = parts
            stored = ("[фото] " + text).strip()
            if self._vision_model is None:  # already guarded by has_vision above
                return Result(text=MSG_NO_VISION)
            model = self._vision_model
        else:
            user_prompt = text
            model = self._text_model

        limits = UsageLimits(total_tokens_limit=self._max_tokens, request_limit=self._max_rounds)
        try:
            result = await self._agent.run(
                user_prompt,
                deps=deps,
                message_history=_to_message_history(history),
                model=model,
                usage_limits=limits,
            )
        except _AskUser as ask:
            await self._persist(user_id, stored, ask.result.text)
            return ask.result
        except UsageLimitExceeded:
            logger.warning("usage limit tripped for user %d", user_id)
            return Result(text=MSG_BUDGET_SPENT)

        answer = (result.output or "").strip()
        await self._persist(user_id, stored, answer)
        return Result(text=answer)

    # ---- internals --------------------------------------------------------

    async def _remember(self, deps: _Deps, key: str, value: str) -> str:
        key = _collapse(key)
        if not key:
            return "ERROR: key обязателен и не может быть пустым"
        if len(key) > MAX_PREF_KEY_LEN:
            return f"ERROR: key слишком длинный (макс {MAX_PREF_KEY_LEN} символов)"
        value = _collapse(value)
        if not value:
            return "ERROR: value обязателен для remember_preference"
        if len(value) > MAX_PREF_VALUE_LEN:
            return (
                f"ERROR: value слишком длинный (макс {MAX_PREF_VALUE_LEN} символов); "
                "сохрани короче и по сути"
            )
        await deps.store.set_preference(deps.user_id, key, value)
        await self._maybe_consolidate(deps.user_id)
        return f"OK: сохранено {key} = {value}"

    async def _maybe_consolidate(self, user_id: int) -> None:
        current = await self._store.preferences(user_id)
        if len(current) < CONSOLIDATE_THRESHOLD:
            return
        payload = json.dumps(
            {"preferences": [{"key": p.key, "value": p.value} for p in current]},
            ensure_ascii=False,
        )
        text = await self._aux_run(self._consolidate_agent, payload, max_tokens=800)
        if not text:
            return
        desired = _parse_consolidated(text)
        if not desired or len(desired) > len(current):
            return
        keep = {p.key for p in desired}
        for p in current:
            if p.key not in keep:
                await self._store.delete_preference(user_id, p.key)
        for p in desired:
            await self._store.set_preference(user_id, p.key, p.value)

    async def _condense_history(self, user_id: int, epoch: int) -> tuple[str, list[Message]]:
        if self._summary_char_budget < 0:
            return "", await self._store.recent_messages(user_id, self._history_depth)

        summary, up_to = await self._store.get_session_summary(user_id, epoch)
        if up_to < epoch:
            up_to = epoch
        tail = await self._store.messages_since(user_id, up_to, MAX_UNSUMMARIZED_SCAN)

        total = sum(len(m.content) for m in tail)  # runes
        if total <= self._summary_char_budget or len(tail) <= SUMMARY_KEEP_RECENT:
            return summary, tail

        foldable = tail[: len(tail) - SUMMARY_KEEP_RECENT]
        recent = tail[len(tail) - SUMMARY_KEEP_RECENT :]
        nxt = await self._summarize_into(summary, foldable)
        if not nxt:
            return summary, recent  # bounded window, existing summary stands
        await self._store.put_session_summary(user_id, epoch, nxt, foldable[-1].id)
        return nxt, recent

    async def _summarize_into(self, prev: str, msgs: list[Message]) -> str:
        if not msgs:
            return prev
        buf = ""
        if prev:
            buf = f"Предыдущее краткое содержание:\n{prev}\n\nНовые сообщения диалога:\n"
        for m in msgs:
            who = "Ассистент" if m.role == "assistant" else "Пользователь"
            buf += f"{who}: {m.content}\n"
        text = await self._aux_run(self._summary_agent, buf, max_tokens=500)
        return text[:SUMMARY_MAX_CHARS]

    async def _aux_run(self, agent: Agent[None, str], prompt: str, *, max_tokens: int) -> str:
        try:
            res = await agent.run(prompt, model_settings=ModelSettings(max_tokens=max_tokens))
        except Exception:
            logger.debug("aux completion failed", exc_info=True)
            return ""
        return (res.output or "").strip()

    async def _persist(self, user_id: int, user_text: str, answer: str) -> None:
        for role, content in (("user", user_text), ("assistant", answer)):
            if not content:
                continue
            try:
                await self._store.append_message(user_id, role, content)
            except Exception:
                logger.warning("store %s message failed", role, exc_info=True)


async def _build_moysklad_tools(fastmcp: FastMCP) -> list[Tool[_Deps]]:
    """Forward each MCP tool to the in-process FastMCP server, reusing its schema."""
    tools: list[Tool[_Deps]] = []
    for t in await fastmcp.list_tools():
        tools.append(_forward_tool(fastmcp, t.name, t.description or "", t.inputSchema))
    return tools


def _forward_tool(
    fastmcp: FastMCP, name: str, description: str, schema: dict[str, object]
) -> Tool[_Deps]:
    async def forward(**kwargs: object) -> str:
        try:
            # FastMCP.call_tool returns (content_blocks, structured_content).
            result = cast("tuple[Any, dict[str, Any]]", await fastmcp.call_tool(name, kwargs))
        except Exception as exc:
            return f"ERROR: tool call failed: {exc}"
        return _truncate(json.dumps(result[1], ensure_ascii=False), MAX_TOOL_RESULT_CHARS)

    return Tool.from_schema(forward, name=name, description=description, json_schema=schema)
