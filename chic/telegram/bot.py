"""aiogram webhook bot wiring the LLM agent to Telegram.

Owns policy (allowlist, dedupe, commands, keyboards); the agent owns the answer.
Updates arrive via :meth:`feed_update` (the FastAPI webhook endpoint calls it
after verifying the secret-token header).
"""

from __future__ import annotations

import contextlib
import logging
from collections.abc import Awaitable, Callable
from typing import Any

from aiogram import Bot, Dispatcher, F, Router
from aiogram.exceptions import TelegramBadRequest
from aiogram.filters import Command
from aiogram.types import BotCommand, CallbackQuery, Message, Update, User
from aiogram.utils.chat_action import ChatActionSender
from opentelemetry import trace

from chic.agent import ChicAgent, Result
from chic.telegram.dedupe import Dedupe
from chic.telegram.keyboards import (
    CLARIFY_CUSTOM,
    MEM_FORGET_PREFIX,
    MSG_ASK_CUSTOM,
    MSG_FEEDBACK_THANKS,
    MSG_MENU_PROMPT,
    button_label,
    clarify_keyboard,
    feedback_keyboard,
    index_from_callback,
    menu_keyboard,
    parse_feedback,
    render_memory,
    template_by_callback,
)
from chic.telegram.photo import PhotoTooLargeError, photo_data_uri
from chic.telegram.render import source_chunks, to_markdown_v2
from chic.tracing import (
    CHAIN,
    add_event,
    mark_input,
    mark_output,
    set_status,
    span_id_hex,
)

logger = logging.getLogger(__name__)

MSG_SESSION_RESET = "🆕 Начали заново — прошлый диалог больше не учитывается."
MSG_PRIVATE = "Извините, этот бот приватный."
MSG_ERROR = "Что-то пошло не так, попробуйте ещё раз."
MSG_PHOTO_TOO_LARGE = (
    "Фото слишком большое (более 20 МБ). Пришлите файл поменьше или задайте вопрос текстом."
)
MSG_NO_TEXT = "Я понимаю текст и фотографии."

# (span_id, rating, user_id, chat_id) — records a 👍/👎 rating (Phoenix annotation).
FeedbackHook = Callable[[str, str, int, int], Awaitable[None]]

_COMMANDS = [
    BotCommand(command="menu", description="Готовые вопросы"),
    BotCommand(command="memory", description="Что бот о вас помнит"),
    BotCommand(command="new", description="Новый диалог — забыть контекст"),
]


async def _allowlist(
    handler: Callable[[Any, dict[str, Any]], Awaitable[Any]],
    event: Message | CallbackQuery,
    data: dict[str, Any],
    *,
    allowed: set[int],
) -> Any:
    user: User | None = data.get("event_from_user")
    if user is None or user.id not in allowed:
        if isinstance(event, Message):
            with contextlib.suppress(TelegramBadRequest):
                await event.answer(MSG_PRIVATE)
        elif isinstance(event, CallbackQuery):
            with contextlib.suppress(TelegramBadRequest):
                await event.answer()
        return None
    return await handler(event, data)


class ChicBot:
    def __init__(
        self,
        *,
        token: str,
        webhook_secret: str,
        allowed_ids: set[int],
        agent: ChicAgent,
        on_feedback: FeedbackHook | None = None,
        tracer: trace.Tracer | None = None,
    ) -> None:
        self._secret = webhook_secret
        self._allowed = allowed_ids
        self._agent = agent
        self._on_feedback = on_feedback
        self._tracer = tracer
        self._dedupe = Dedupe(1024)
        self._bot = Bot(token)
        self._dp = Dispatcher()

        async def allow_mw(handler: Any, event: Any, data: Any) -> Any:
            return await _allowlist(handler, event, data, allowed=self._allowed)

        self._dp.message.middleware(allow_mw)
        self._dp.callback_query.middleware(allow_mw)

        router = Router()
        router.message.register(self._cmd_menu, Command("menu", "start", "help"))
        router.message.register(self._cmd_memory, Command("memory"))
        router.message.register(self._cmd_new, Command("new"))
        router.message.register(self._on_message)
        router.callback_query.register(self._cb_feedback, F.data.startswith("fb:"))
        router.callback_query.register(self._cb_quick, F.data.startswith("ask:"))
        router.callback_query.register(self._cb_clarify, F.data.startswith("cq:"))
        router.callback_query.register(self._cb_memory_forget, F.data.startswith("mf:"))
        self._dp.include_router(router)

    # ---- lifecycle --------------------------------------------------------

    @property
    def webhook_secret(self) -> str:
        return self._secret

    async def register_webhook(self, url: str) -> None:
        await self._bot.set_webhook(
            url, secret_token=self._secret, allowed_updates=["message", "callback_query"]
        )
        await self._bot.set_my_commands(_COMMANDS)

    async def feed_update(self, data: dict[str, Any]) -> None:
        update = Update.model_validate(data, context={"bot": self._bot})
        if not self._dedupe.first_seen(update.update_id):
            logger.debug("duplicate update %d dropped", update.update_id)
            return
        await self._dp.feed_update(self._bot, update)

    async def me(self) -> User:
        return await self._bot.get_me()

    async def send(self, chat_id: int, text: str) -> None:
        """Proactively push a message (e.g. the scheduled digest). HTML-formatted."""
        await self._bot.send_message(chat_id, text, parse_mode="HTML")

    async def close(self) -> None:
        await self._bot.session.close()

    # ---- commands ---------------------------------------------------------

    async def _cmd_menu(self, message: Message) -> None:
        await message.answer(MSG_MENU_PROMPT, reply_markup=menu_keyboard())

    async def _cmd_memory(self, message: Message) -> None:
        if message.from_user is None:
            return
        await self._show_memory(message.from_user.id, message.chat.id)

    async def _cmd_new(self, message: Message) -> None:
        if message.from_user is None:
            return
        await self._agent.reset(message.from_user.id)
        await message.answer(MSG_SESSION_RESET)

    async def _on_message(self, message: Message) -> None:
        if message.from_user is None:
            return
        user_id = message.from_user.id
        chat_id = message.chat.id
        text = message.text or ""
        image = ""
        if message.photo:
            try:
                image = await photo_data_uri(self._bot, message)
            except PhotoTooLargeError:
                await message.answer(MSG_PHOTO_TOO_LARGE)
                return
            except Exception:
                logger.exception("photo download failed")
                await message.answer(MSG_ERROR)
                return
            text = message.caption or ""
        if not text and not image:
            await message.answer(MSG_NO_TEXT)
            return
        await self._run_and_deliver(user_id, chat_id, text, image)

    # ---- callbacks --------------------------------------------------------

    async def _cb_feedback(self, cb: CallbackQuery) -> None:
        parsed = parse_feedback(cb.data or "")
        if parsed is None:
            await cb.answer()
            return
        rating, span_id = parsed
        await cb.answer(MSG_FEEDBACK_THANKS)
        if self._on_feedback is not None:
            chat_id = cb.message.chat.id if cb.message else 0
            with contextlib.suppress(Exception):
                await self._on_feedback(span_id, rating, cb.from_user.id, chat_id)
        if isinstance(cb.message, Message):
            with contextlib.suppress(TelegramBadRequest):
                await cb.message.edit_reply_markup(reply_markup=None)

    async def _cb_quick(self, cb: CallbackQuery) -> None:
        await cb.answer()
        question = template_by_callback(cb.data or "")
        if question is None or not isinstance(cb.message, Message):
            return
        chat_id = cb.message.chat.id
        await self._bot.send_message(chat_id, "❓ " + question)
        await self._run_and_deliver(cb.from_user.id, chat_id, question)

    async def _cb_clarify(self, cb: CallbackQuery) -> None:
        await cb.answer()
        if not isinstance(cb.message, Message):
            return
        chat_id = cb.message.chat.id
        data = cb.data or ""
        if data == CLARIFY_CUSTOM:
            with contextlib.suppress(TelegramBadRequest):
                await cb.message.edit_reply_markup(reply_markup=None)
            await self._bot.send_message(chat_id, MSG_ASK_CUSTOM)
            return
        label = button_label(cb.message.reply_markup, data)
        if not label:
            with contextlib.suppress(TelegramBadRequest):
                await cb.message.edit_reply_markup(reply_markup=None)
            return
        await self._mark_answered(cb.message, label)
        await self._run_and_deliver(cb.from_user.id, chat_id, label)

    async def _cb_memory_forget(self, cb: CallbackQuery) -> None:
        await cb.answer()
        if not isinstance(cb.message, Message):
            return
        idx = index_from_callback(cb.data or "", MEM_FORGET_PREFIX)
        if idx is None:
            return
        user_id = cb.from_user.id
        prefs = await self._agent.preferences(user_id)
        if idx < len(prefs):
            await self._agent.forget_preference(user_id, prefs[idx].key)
            prefs = await self._agent.preferences(user_id)
        text, markup = render_memory([(p.key, p.value) for p in prefs])
        with contextlib.suppress(TelegramBadRequest):
            await cb.message.edit_text(text, parse_mode="HTML", reply_markup=markup)

    # ---- delivery ---------------------------------------------------------

    async def _run_and_deliver(
        self, user_id: int, chat_id: int, text: str, image: str = ""
    ) -> None:
        span_id = ""
        session_id = await self._agent.session_key(user_id) if self._tracer is not None else ""
        async with ChatActionSender.typing(bot=self._bot, chat_id=chat_id):
            cm: contextlib.AbstractContextManager[trace.Span | None] = (
                self._tracer.start_as_current_span("telegram.message")
                if self._tracer is not None
                else contextlib.nullcontext()
            )
            with cm as span:
                span_id = span_id_hex(span)
                mark_input(
                    span,
                    kind=CHAIN,
                    value=text or ("[фото]" if image else ""),
                    session_id=session_id,
                    user_id=str(user_id),
                    metadata={"has_image": bool(image)},
                )
                try:
                    result = await self._agent.handle(user_id, text, image)
                except Exception:
                    logger.exception("agent handle failed")
                    set_status(span, ok=False, description="agent handle failed")
                    await self._bot.send_message(chat_id, MSG_ERROR)
                    return
                mark_output(span, value=result.text)
                if result.options:
                    # A clarifying question is a normal turn, not a failure.
                    add_event(span, "clarify_requested")
                set_status(span, ok=True)
        await self._deliver(chat_id, result, span_id)

    async def _deliver(self, chat_id: int, result: Result, span_id: str) -> None:
        if result.options:
            await self._send_chunks(
                chat_id, result.text, clarify_keyboard(result.options, result.allow_custom)
            )
            return
        if not result.text:
            return
        await self._send_chunks(chat_id, result.text, feedback_keyboard(span_id))

    async def _send_chunks(self, chat_id: int, text: str, markup: Any) -> None:
        chunks = source_chunks(text)
        for i, chunk in enumerate(chunks):
            last = i == len(chunks) - 1
            reply_markup = markup if last else None
            try:
                await self._bot.send_message(
                    chat_id,
                    to_markdown_v2(chunk),
                    parse_mode="MarkdownV2",
                    reply_markup=reply_markup,
                )
            except TelegramBadRequest:
                # Fallback: send the raw source text as-is (no custom parser).
                logger.warning("MarkdownV2 rejected, resending raw", exc_info=True)
                with contextlib.suppress(TelegramBadRequest):
                    await self._bot.send_message(chat_id, chunk, reply_markup=reply_markup)

    async def _show_memory(self, user_id: int, chat_id: int) -> None:
        prefs = await self._agent.preferences(user_id)
        text, markup = render_memory([(p.key, p.value) for p in prefs])
        await self._bot.send_message(chat_id, text, parse_mode="HTML", reply_markup=markup)

    async def _mark_answered(self, message: Message, choice: str) -> None:
        question = (message.text or "").strip()
        text = (question + "\n\n" if question else "") + "✅ " + choice
        with contextlib.suppress(TelegramBadRequest):
            await message.edit_text(text)  # no markup ⇒ drops the keyboard
