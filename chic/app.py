"""FastAPI application: healthz, the Telegram webhook, and full startup wiring.

Startup readiness is decoupled from external services: the lifespan does only
fast local setup (tracing install, SQLite migrate, client/agent/bot build) and
yields immediately, so ``/healthz`` and the socket come up in ~1s. The
network-dependent steps — resolving the account currency (MoySklad) and
registering the Telegram webhook — run in a background task, so a slow or
firewalled upstream can never block the health check (and fail the deploy).

Set ``wire=False`` for a routes-only app (tests): no network, no bot.
"""

from __future__ import annotations

import asyncio
import contextlib
import logging
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException, Request
from opentelemetry import trace

from chic.agent import ChicAgent
from chic.buildinfo import APP_VERSION, build_info
from chic.cache import CacheStore, CachingClient, Source
from chic.config import Settings, get_settings
from chic.logging import setup_logging
from chic.mcpserver import build_server
from chic.moysklad import MoyskladClient
from chic.store import Store
from chic.telegram import ChicBot
from chic.tracing import PhoenixAnnotator, configure_tracing

logger = logging.getLogger(__name__)


async def _resolve_currency(api: Source) -> tuple[str, str]:
    try:
        cur = await api.account_currency()
    except Exception:
        logger.warning("account currency lookup failed; using neutral labels", exc_info=True)
        return "", ""
    return cur.iso_code, cur.name


async def _build_agent(settings: Settings, store: Store, api: Source) -> ChicAgent:
    # Built with neutral currency; resolved in the background after startup.
    return await ChicAgent.create(
        fastmcp=build_server(api),
        store=store,
        deepseek_api_key=settings.deepseek_api_key,
        deepseek_model=settings.deepseek_model,
        deepseek_base_url=settings.deepseek_base_url,
        openai_api_key=settings.openai_api_key,
        openai_model=settings.openai_model,
        openai_base_url=settings.openai_base_url,
        summary_char_budget=settings.summary_char_budget,
    )


def _make_bot(settings: Settings, agent: ChicAgent) -> ChicBot:
    annotator = PhoenixAnnotator(settings.phoenix_collector_endpoint)

    async def on_feedback(span_id: str, rating: str, user_id: int, _chat_id: int) -> None:
        like = rating == "like"
        await annotator.annotate(
            span_id,
            name="user_feedback",
            label="thumbs_up" if like else "thumbs_down",
            score=1.0 if like else 0.0,
            identifier=f"{user_id}:{span_id}",
        )

    tracer = trace.get_tracer("chic.telegram") if settings.tracing_enabled else None
    return ChicBot(
        token=settings.telegram_bot_token,
        webhook_secret=settings.telegram_webhook_secret,
        allowed_ids=settings.allowed_ids,
        agent=agent,
        on_feedback=on_feedback,
        tracer=tracer,
    )


async def _startup_network(settings: Settings, agent: ChicAgent, bot: ChicBot, api: Source) -> None:
    """Best-effort, off the critical path: currency labels + webhook registration."""
    code, name = await _resolve_currency(api)
    if code:
        agent.set_currency(code, name)
    try:
        url = settings.public_base_url.rstrip("/") + "/tg/" + settings.telegram_webhook_secret
        await bot.register_webhook(url)
        me = await bot.me()
        logger.info("bot @%s ready, webhook registered", me.username)
    except Exception:
        logger.exception("webhook registration failed")


@asynccontextmanager
async def _lifespan(app: FastAPI) -> AsyncIterator[None]:
    settings = get_settings()
    shutdown_tracing = configure_tracing(
        endpoint=settings.phoenix_collector_endpoint,
        service_name="chic-bot",
        service_version=APP_VERSION,
        environment=settings.app_env,
    )
    # All local, fast: never blocks the health check.
    store = await Store.open(settings.app_db)
    ms_client = MoyskladClient(settings.moysklad_token)
    cache_store: CacheStore | None = None
    api: Source = ms_client
    if settings.cache_db:
        cache_store = await CacheStore.open(settings.cache_db)
        cache_store.start_janitor(600)
        api = CachingClient(ms_client, cache_store)

    agent = await _build_agent(settings, store, api)
    bot = _make_bot(settings, agent)
    app.state.bot = bot

    # Network I/O runs after we yield, so the socket binds and /healthz answers now.
    bg = asyncio.create_task(_startup_network(settings, agent, bot, api))

    try:
        yield
    finally:
        bg.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await bg
        await bot.close()
        await ms_client.aclose()
        if cache_store is not None:
            await cache_store.close()
        await store.close()
        shutdown_tracing()


def create_app(*, wire: bool = True) -> FastAPI:
    settings = get_settings()
    setup_logging(settings.log_format, settings.log_level)

    app = FastAPI(title="chic", version=APP_VERSION, lifespan=_lifespan if wire else None)

    @app.get("/healthz")
    async def healthz() -> dict[str, Any]:
        return {"status": "ok", "build": build_info()}

    @app.post("/tg/{secret}")
    async def telegram_webhook(secret: str, request: Request) -> dict[str, bool]:
        bot: ChicBot | None = getattr(app.state, "bot", None)
        if bot is None:
            raise HTTPException(status_code=503, detail="bot not ready")
        # Path secret is a first barrier; the header check is authoritative.
        header = request.headers.get("X-Telegram-Bot-Api-Secret-Token", "")
        if secret != bot.webhook_secret or header != bot.webhook_secret:
            raise HTTPException(status_code=403, detail="bad secret")
        await bot.feed_update(await request.json())
        return {"ok": True}

    return app
