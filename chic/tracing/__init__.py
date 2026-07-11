"""OpenTelemetry + OpenInference tracing to Arize Phoenix (optional)."""

from chic.tracing.phoenix import PhoenixAnnotator
from chic.tracing.setup import configure_tracing, span_id_hex

__all__ = ["PhoenixAnnotator", "configure_tracing", "span_id_hex"]
