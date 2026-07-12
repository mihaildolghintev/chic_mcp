"""OpenTelemetry setup via the official Phoenix SDK (``arize-phoenix-otel``).

``phoenix.otel.register`` builds the Resource, OTLP/HTTP exporter, batch processor
and installs the global ``TracerProvider`` in one call. pydantic-ai is not an
``auto_instrument`` target (the OpenInference package ships no entry point), so its
spans are still enriched by ``OpenInferenceSpanProcessor`` and emitted by
``Agent.instrument_all`` — exactly as Phoenix's Pydantic AI integration prescribes.

No-op when ``PHOENIX_COLLECTOR_ENDPOINT`` is unset — spans are never exported and
``configure_tracing`` returns a no-op shutdown.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from opentelemetry.trace import Span, format_span_id

logger = logging.getLogger(__name__)


def span_id_hex(span: Span | None) -> str:
    """16-hex span id for a recording span, else "" (drives the 👍/👎 affordance)."""
    if span is None:
        return ""
    ctx = span.get_span_context()
    if not ctx.span_id:
        return ""
    return format_span_id(ctx.span_id)


def configure_tracing(
    *,
    endpoint: str | None,
    service_name: str = "chic-bot",
    service_version: str = "dev",
    environment: str = "local",
) -> Callable[[], None]:
    """Install a global TracerProvider exporting to Phoenix. Returns shutdown()."""
    endpoint = (endpoint or "").strip()
    if not endpoint:
        logger.info("tracing disabled (no PHOENIX_COLLECTOR_ENDPOINT)")
        return lambda: None

    try:
        from openinference.instrumentation.pydantic_ai import OpenInferenceSpanProcessor
        from opentelemetry.sdk.resources import Resource
        from phoenix.otel import register
        from pydantic_ai import Agent
        from pydantic_ai.models.instrumented import InstrumentationSettings

        # register() merges project_name into whatever resource we pass, so version
        # and environment ride along on every span.
        resource = Resource.create(
            {
                "service.name": service_name,
                "service.version": service_version,
                "deployment.environment": environment,
            }
        )
        tracer_provider = register(
            endpoint=endpoint.rstrip("/") + "/v1/traces",
            protocol="http/protobuf",
            project_name=service_name,
            resource=resource,
            batch=True,
            set_global_tracer_provider=True,
            verbose=False,
        )
        # pydantic-ai spans are enriched into OpenInference conventions here. Kept
        # (replace_default_processor=False) alongside register()'s exporter; the
        # batch exporter serialises lazily, after this processor mutates the span.
        # register() is typed to the base TracerProvider; the object it returns is
        # Phoenix's subclass, which adds replace_default_processor.
        tracer_provider.add_span_processor(  # type: ignore[call-arg]
            OpenInferenceSpanProcessor(), replace_default_processor=False
        )
        # Make every agent emit OTel spans. Pinned to v5 (pydantic-ai's current
        # default; v1 is gone): openinference-instrumentation-pydantic-ai 0.1.17
        # enriches it identically to v2 (verified: same LLM/AGENT spans, messages
        # and token counts). Pinning — not floating on the default — guards against
        # a future default bump to a format OpenInference hasn't caught up to.
        Agent.instrument_all(InstrumentationSettings(version=5))
    except Exception:
        # Observability must never crash the app.
        logger.exception("tracing setup failed; continuing without it")
        return lambda: None

    logger.info("tracing enabled → %s", endpoint)
    return tracer_provider.shutdown
