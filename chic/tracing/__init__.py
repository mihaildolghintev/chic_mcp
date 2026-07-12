"""Tracing to Arize Phoenix via the official SDKs (arize-phoenix-otel/client)."""

from chic.tracing.phoenix import PhoenixAnnotator
from chic.tracing.setup import configure_tracing, span_id_hex

__all__ = ["PhoenixAnnotator", "configure_tracing", "span_id_hex"]
