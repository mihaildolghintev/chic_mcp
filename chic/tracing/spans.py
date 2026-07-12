"""Stamp OpenInference semantics onto manually-created spans.

Two call sites use this. The ``telegram.message`` root is a CHAIN carrying the
user's message in/out plus the session and user ids (what Phoenix reads to fill
the trace table and to group a conversation in the Sessions view). The MoySklad
HTTP calls are TOOL spans carrying the raw request/response payload — the layer
below FastMCP's structured result, so an empty ``rows: []`` can be traced back to
the exact URL, query params and status the account returned.

Everything degrades to a no-op when tracing is off (a non-recording span) or the
OpenInference semantic-convention package isn't importable — observability must
never crash a call path. Headers are never recorded, so the MoySklad bearer token
cannot leak into a trace.
"""

from __future__ import annotations

import json
from collections.abc import Iterator, Mapping, Sequence
from contextlib import contextmanager
from typing import Any, TypeGuard

from opentelemetry import trace
from opentelemetry.trace import Span
from opentelemetry.trace.status import Status, StatusCode

_TEXT = "text/plain"
_JSON = "application/json"

# Cap raw HTTP bodies so a large page of rows can't bloat a trace.
MAX_HTTP_BODY_CHARS = 16_000

try:
    from openinference.semconv.trace import (
        OpenInferenceSpanKindValues,
        SpanAttributes,
    )

    CHAIN = OpenInferenceSpanKindValues.CHAIN.value
    TOOL = OpenInferenceSpanKindValues.TOOL.value
    _KIND = SpanAttributes.OPENINFERENCE_SPAN_KIND
    _IN = SpanAttributes.INPUT_VALUE
    _IN_MIME = SpanAttributes.INPUT_MIME_TYPE
    _OUT = SpanAttributes.OUTPUT_VALUE
    _OUT_MIME = SpanAttributes.OUTPUT_MIME_TYPE
    _SESSION = SpanAttributes.SESSION_ID
    _USER = SpanAttributes.USER_ID
    _META = SpanAttributes.METADATA
    _AVAILABLE = True
except Exception:  # pragma: no cover - defensive; keeps tracing optional
    CHAIN = "CHAIN"
    TOOL = "TOOL"
    _KIND = _IN = _IN_MIME = _OUT = _OUT_MIME = _SESSION = _USER = _META = ""
    _AVAILABLE = False


def _live(span: Span | None) -> TypeGuard[Span]:
    return span is not None and span.is_recording()


def mark_input(
    span: Span | None,
    *,
    kind: str,
    value: str,
    mime: str = _TEXT,
    session_id: str = "",
    user_id: str = "",
    metadata: Mapping[str, Any] | None = None,
) -> None:
    """Set OpenInference kind/input plus session and user ids on ``span``."""
    if not _AVAILABLE or not _live(span):
        return
    span.set_attribute(_KIND, kind)
    span.set_attribute(_IN, value)
    span.set_attribute(_IN_MIME, mime)
    if session_id:
        span.set_attribute(_SESSION, session_id)
    if user_id:
        span.set_attribute(_USER, user_id)
    if metadata:
        span.set_attribute(_META, json.dumps(dict(metadata), ensure_ascii=False, default=str))


def mark_output(span: Span | None, *, value: str, mime: str = _TEXT) -> None:
    if not _AVAILABLE or not _live(span):
        return
    span.set_attribute(_OUT, value)
    span.set_attribute(_OUT_MIME, mime)


def set_status(span: Span | None, *, ok: bool, description: str = "") -> None:
    """OK/ERROR on the span so Phoenix's status column and error filter work."""
    if not _live(span):
        return
    span.set_status(Status(StatusCode.OK if ok else StatusCode.ERROR, description or None))


def add_event(span: Span | None, name: str) -> None:
    if not _live(span):
        return
    span.add_event(name)


@contextmanager
def http_span(
    method: str, path: str, url: str, params: Sequence[tuple[str, str]]
) -> Iterator[Span | None]:
    """A TOOL span around one outbound API request (no headers recorded)."""
    tracer = trace.get_tracer("chic.moysklad")
    with tracer.start_as_current_span(f"{method} {path}") as span:
        mark_input(
            span,
            kind=TOOL,
            value=json.dumps(
                {"method": method, "url": url, "params": [list(p) for p in params]},
                ensure_ascii=False,
                default=str,
            ),
            mime=_JSON,
        )
        yield span


def record_http_response(span: Span | None, status: int, body: bytes) -> None:
    """Attach the response status + (truncated) body; OK status only for 2xx.

    Non-2xx bodies are still recorded, but the status is left for the raising
    caller — ``http_span`` sets ERROR automatically when an exception unwinds it.
    """
    if not _AVAILABLE or not _live(span):
        return
    span.set_attribute("http.response.status_code", status)
    text = body.decode("utf-8", "replace")
    if len(text) > MAX_HTTP_BODY_CHARS:
        text = text[:MAX_HTTP_BODY_CHARS] + "…[truncated]"
    span.set_attribute(_OUT, text)
    span.set_attribute(_OUT_MIME, _JSON)
    if 200 <= status < 300:
        span.set_status(Status(StatusCode.OK))
