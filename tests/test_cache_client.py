from __future__ import annotations

from chic.cache.client import CachingClient, TTLs
from chic.cache.store import CacheStore
from chic.moysklad.models import Product
from chic.moysklad.options import ListOptions


class FakeSource:
    """Minimal Source stub that counts calls and returns canned products."""

    def __init__(self) -> None:
        self.calls = 0

    async def list_products(self, opts: ListOptions) -> list[Product]:
        self.calls += 1
        return [Product(id="p1", name=f"query={opts.search}")]


async def test_cache_hit_avoids_second_fetch() -> None:
    store = await CacheStore.open(":memory:")
    src = FakeSource()
    client = CachingClient(src, store)  # type: ignore[arg-type]  # structural Source
    try:
        first = await client.list_products(ListOptions(search="a"))
        second = await client.list_products(ListOptions(search="a"))
        assert first == second
        assert src.calls == 1  # served from cache the second time
    finally:
        await store.close()


async def test_different_params_are_separate_entries() -> None:
    store = await CacheStore.open(":memory:")
    src = FakeSource()
    client = CachingClient(src, store)  # type: ignore[arg-type]
    try:
        await client.list_products(ListOptions(search="a"))
        await client.list_products(ListOptions(search="b"))
        assert src.calls == 2  # distinct keys ⇒ two fetches
    finally:
        await store.close()


async def test_zero_ttl_disables_caching() -> None:
    store = await CacheStore.open(":memory:")
    src = FakeSource()
    client = CachingClient(src, store, TTLs(products=0))  # type: ignore[arg-type]
    try:
        await client.list_products(ListOptions(search="a"))
        await client.list_products(ListOptions(search="a"))
        assert src.calls == 2  # no caching
    finally:
        await store.close()
