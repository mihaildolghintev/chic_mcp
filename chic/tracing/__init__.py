"""Tracing to Arize Phoenix via the official SDKs (arize-phoenix-otel/client)."""

from chic.tracing.phoenix import PhoenixAnnotator
from chic.tracing.setup import configure_tracing, span_id_hex
from chic.tracing.spans import (
    CHAIN,
    TOOL,
    add_event,
    http_span,
    mark_input,
    mark_output,
    record_http_response,
    set_status,
)

__all__ = [
    "CHAIN",
    "TOOL",
    "PhoenixAnnotator",
    "add_event",
    "configure_tracing",
    "http_span",
    "mark_input",
    "mark_output",
    "record_http_response",
    "set_status",
    "span_id_hex",
]
