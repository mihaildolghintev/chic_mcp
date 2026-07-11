"""OpenTelemetry setup: pydantic-ai spans → OpenInference → Phoenix (OTLP/HTTP).

No-op when ``PHOENIX_COLLECTOR_ENDPOINT`` is unset — spans are never exported and
``configure_tracing`` returns a no-op shutdown, mirroring the Go build.
"""

from __future__ import annotations

import logging
from collections.abc import Callable

from opentelemetry import trace
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
        from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
        from opentelemetry.sdk.resources import Resource
        from opentelemetry.sdk.trace import TracerProvider
        from opentelemetry.sdk.trace.export import BatchSpanProcessor
        from pydantic_ai import Agent

        resource = Resource.create(
            {
                "service.name": service_name,
                "service.version": service_version,
                "deployment.environment": environment,
            }
        )
        provider = TracerProvider(resource=resource)
        # Enrich pydantic-ai's native spans into OpenInference conventions, then export.
        provider.add_span_processor(OpenInferenceSpanProcessor())
        provider.add_span_processor(
            BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint.rstrip("/") + "/v1/traces"))
        )
        trace.set_tracer_provider(provider)
        # Make pydantic-ai emit OTel spans (LLM calls, tool calls).
        Agent.instrument_all()
    except Exception:
        # Observability must never crash the app.
        logger.exception("tracing setup failed; continuing without it")
        return lambda: None

    logger.info("tracing enabled → %s", endpoint)

    def shutdown() -> None:
        provider.shutdown()

    return shutdown
