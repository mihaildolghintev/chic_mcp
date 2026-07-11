from __future__ import annotations

import httpx
import pytest
import respx
from aiolimiter import AsyncLimiter
from chic.moysklad import DocumentQuery, ListOptions, MoyskladClient, MoyskladError

BASE = "https://api.test/api/remap/1.2"


def _make_client(sleeps: list[float] | None = None) -> MoyskladClient:
    async def fake_sleep(d: float) -> None:
        if sleeps is not None:
            sleeps.append(d)

    return MoyskladClient(
        "token",
        base_url=BASE,
        http=httpx.AsyncClient(),
        page_limit=2,
        limiter=AsyncLimiter(1000, 1),  # effectively unlimited in tests
        sleep=fake_sleep,
    )


@respx.mock
async def test_pagination_follows_offset_and_parses_minor_units() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        offset = request.url.params.get("offset")
        if offset == "0":
            return httpx.Response(
                200,
                json={
                    "meta": {"size": 3},
                    "rows": [
                        {"id": "a", "name": "A", "buyPrice": {"value": 12345.0}},
                        {"id": "b", "name": "B"},
                    ],
                },
            )
        return httpx.Response(200, json={"meta": {"size": 3}, "rows": [{"id": "c", "name": "C"}]})

    respx.get(f"{BASE}/entity/product").mock(side_effect=handler)
    client = _make_client()
    try:
        products = await client.list_products(ListOptions())
    finally:
        await client.aclose()

    assert [p.id for p in products] == ["a", "b", "c"]
    # Minor units preserved verbatim (conversion happens in aggregate).
    assert products[0].buy_price is not None
    assert products[0].buy_price.value == 12345.0


@respx.mock
async def test_429_retries_and_honors_lognex_ms_header() -> None:
    responses = iter(
        [
            httpx.Response(
                429,
                headers={"X-Lognex-Retry-After": "500"},  # milliseconds
                json={"errors": [{"error": "rate limited"}]},
            ),
            httpx.Response(200, json={"sales": {"count": 3}}),
        ]
    )
    respx.get(f"{BASE}/report/dashboard/month").mock(side_effect=lambda req: next(responses))

    sleeps: list[float] = []
    client = _make_client(sleeps)
    try:
        dashboard = await client.get_dashboard("month")
    finally:
        await client.aclose()

    assert dashboard.sales.count == 3
    assert sleeps == [0.5]  # 500ms header → 0.5s


@respx.mock
async def test_retry_after_seconds_header() -> None:
    responses = iter(
        [
            httpx.Response(429, headers={"Retry-After": "2"}, json={}),
            httpx.Response(200, json={"sales": {"count": 1}}),
        ]
    )
    respx.get(f"{BASE}/report/dashboard/day").mock(side_effect=lambda req: next(responses))

    sleeps: list[float] = []
    client = _make_client(sleeps)
    try:
        await client.get_dashboard("day")
    finally:
        await client.aclose()

    assert sleeps == [2.0]  # seconds header


@respx.mock
async def test_4xx_raises_without_retry() -> None:
    route = respx.get(f"{BASE}/report/dashboard/month").mock(
        return_value=httpx.Response(403, json={"errors": [{"error": "forbidden"}]})
    )
    client = _make_client()
    try:
        with pytest.raises(MoyskladError) as exc:
            await client.get_dashboard("month")
    finally:
        await client.aclose()

    assert exc.value.status_code == 403
    assert "forbidden" in exc.value.message
    assert route.call_count == 1  # no retry on non-429 4xx


@respx.mock
async def test_get_document_escapes_id() -> None:
    captured: dict[str, str] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        # raw_path keeps the percent-encoding (.path decodes it back).
        captured["raw"] = request.url.raw_path.decode()
        return httpx.Response(200, json={"id": "x", "name": "Doc"})

    respx.get(url__regex=rf"{BASE}/entity/demand/.*").mock(side_effect=handler)
    client = _make_client()
    try:
        await client.get_document("demand", "a/b?c", ["positions"])
    finally:
        await client.aclose()

    # The slash and query chars are percent-encoded into a single path segment.
    assert "/entity/demand/a%2Fb%3Fc" in captured["raw"]


@respx.mock
async def test_account_currency_falls_back_to_first_when_filter_ignored() -> None:
    respx.get(f"{BASE}/entity/currency").mock(
        return_value=httpx.Response(
            200,
            json={
                "meta": {"size": 2},
                "rows": [
                    {"id": "1", "name": "лей", "isoCode": "MDL", "default": False},
                    {"id": "2", "name": "евро", "isoCode": "EUR", "default": False},
                ],
            },
        )
    )
    client = _make_client()
    try:
        currency = await client.account_currency()
    finally:
        await client.aclose()

    assert currency.iso_code == "MDL"  # first row, since none is marked default


@respx.mock
async def test_search_documents_builds_moment_filters() -> None:
    captured: dict[str, list[str]] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured["filter"] = request.url.params.get_list("filter")
        return httpx.Response(200, json={"meta": {"size": 0}, "rows": []})

    respx.get(f"{BASE}/entity/invoiceout").mock(side_effect=handler)
    client = _make_client()
    try:
        await client.search_documents(
            "invoiceout", DocumentQuery(from_="2026-07-01", to="2026-07-10")
        )
    finally:
        await client.aclose()

    assert "moment>=2026-07-01 00:00:00" in captured["filter"]
    assert "moment<=2026-07-10 23:59:59" in captured["filter"]  # inclusive end of day
