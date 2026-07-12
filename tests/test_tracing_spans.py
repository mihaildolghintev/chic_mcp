"""The manual span enrichment must emit the OpenInference attributes Phoenix
reads for its trace table (kind/input/output/status) and Sessions view
(session.id), and the MoySklad HTTP span must carry the raw request/response
without ever recording the bearer-token header.
"""

from __future__ import annotations

from collections.abc import Iterator

import httpx
import pytest
import respx
from aiolimiter import AsyncLimiter
from chic.moysklad import ListOptions, MoyskladClient
from chic.tracing import CHAIN, mark_input, mark_output, record_http_response, set_status
from openinference.semconv.trace import OpenInferenceSpanKindValues, SpanAttributes
from opentelemetry import trace as ot
from opentelemetry.sdk.trace import ReadableSpan, TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import StatusCode

BASE = "https://api.test/api/remap/1.2"


@pytest.fixture
def exporter() -> Iterator[InMemorySpanExporter]:
    """Install an in-memory SDK provider globally so ``http_span`` (which resolves
    the tracer from the global provider) and locally-created spans both export."""
    exp = InMemorySpanExporter()
    provider = ot.get_tracer_provider()
    if not isinstance(provider, TracerProvider):
        provider = TracerProvider()
        ot.set_tracer_provider(provider)
    provider.add_span_processor(SimpleSpanProcessor(exp))
    yield exp
    exp.clear()


def _only(exporter: InMemorySpanExporter, name: str) -> ReadableSpan:
    spans = [s for s in exporter.get_finished_spans() if s.name == name]
    assert len(spans) == 1, f"expected one {name!r} span, got {[s.name for s in spans]}"
    return spans[0]


def test_root_span_carries_chain_input_output_session(exporter: InMemorySpanExporter) -> None:
    tracer = ot.get_tracer("test")
    with tracer.start_as_current_span("telegram.message") as span:
        mark_input(
            span,
            kind=CHAIN,
            value="сколько отгрузок сегодня",
            session_id="42:7",
            user_id="42",
            metadata={"has_image": False},
        )
        mark_output(span, value="Сегодня 3 отгрузки.")
        set_status(span, ok=True)

    attrs = dict(_only(exporter, "telegram.message").attributes or {})
    assert attrs[SpanAttributes.OPENINFERENCE_SPAN_KIND] == OpenInferenceSpanKindValues.CHAIN.value
    assert attrs[SpanAttributes.INPUT_VALUE] == "сколько отгрузок сегодня"
    assert attrs[SpanAttributes.OUTPUT_VALUE] == "Сегодня 3 отгрузки."
    assert attrs[SpanAttributes.SESSION_ID] == "42:7"
    assert attrs[SpanAttributes.USER_ID] == "42"
    assert _only(exporter, "telegram.message").status.status_code == StatusCode.OK


def test_failed_status_marks_error(exporter: InMemorySpanExporter) -> None:
    tracer = ot.get_tracer("test")
    with tracer.start_as_current_span("telegram.message") as span:
        set_status(span, ok=False, description="agent handle failed")
    assert _only(exporter, "telegram.message").status.status_code == StatusCode.ERROR


def test_helpers_are_noops_on_none() -> None:
    # Tracing disabled ⇒ span is None; enrichment must never raise.
    mark_input(None, kind=CHAIN, value="x", session_id="1:0", user_id="1")
    mark_output(None, value="y")
    set_status(None, ok=True)


@respx.mock
async def test_moysklad_get_emits_http_span_with_payload(exporter: InMemorySpanExporter) -> None:
    respx.get(f"{BASE}/entity/product").mock(
        return_value=httpx.Response(200, json={"meta": {"size": 1}, "rows": [{"id": "a"}]})
    )
    client = MoyskladClient(
        "secret-token",
        base_url=BASE,
        http=httpx.AsyncClient(),
        limiter=AsyncLimiter(1000, 1),
    )
    try:
        await client.list_products(ListOptions())
    finally:
        await client.aclose()

    span = _only(exporter, "GET /entity/product")
    attrs = dict(span.attributes or {})
    assert attrs[SpanAttributes.OPENINFERENCE_SPAN_KIND] == OpenInferenceSpanKindValues.TOOL.value
    assert attrs["http.response.status_code"] == 200
    assert '"id":"a"' in str(attrs[SpanAttributes.OUTPUT_VALUE])
    assert span.status.status_code == StatusCode.OK
    # The bearer token must never leak into a trace.
    serialized = repr(attrs)
    assert "secret-token" not in serialized
    assert "Authorization" not in serialized


def test_record_http_response_error_body_without_ok(exporter: InMemorySpanExporter) -> None:
    tracer = ot.get_tracer("test")
    with tracer.start_as_current_span("GET /x") as span:
        record_http_response(span, 500, b'{"errors":[{"error":"boom"}]}')
    attrs = dict(_only(exporter, "GET /x").attributes or {})
    assert attrs["http.response.status_code"] == 500
    assert "boom" in str(attrs[SpanAttributes.OUTPUT_VALUE])
    # 5xx leaves status Unset here — the raising caller / http_span sets ERROR.
    assert _only(exporter, "GET /x").status.status_code == StatusCode.UNSET
