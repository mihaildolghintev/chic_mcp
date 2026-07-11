from __future__ import annotations

from chic.cache.store import CacheStore


class Clock:
    def __init__(self, t: float = 1000.0) -> None:
        self.t = t

    def __call__(self) -> float:
        return self.t


async def test_set_get_roundtrip() -> None:
    store = await CacheStore.open(":memory:")
    try:
        await store.set("k", b"hello", ttl=60)
        assert await store.get("k") == b"hello"
        assert await store.get("missing") is None
    finally:
        await store.close()


async def test_expiry() -> None:
    clock = Clock(1000.0)
    store = await CacheStore.open(":memory:", now=clock)
    try:
        await store.set("k", b"v", ttl=10)
        assert await store.get("k") == b"v"
        clock.t = 1011.0  # past expiry
        assert await store.get("k") is None
    finally:
        await store.close()


async def test_zero_ttl_is_noop() -> None:
    store = await CacheStore.open(":memory:")
    try:
        await store.set("k", b"v", ttl=0)
        assert await store.get("k") is None
    finally:
        await store.close()


async def test_purge_removes_expired() -> None:
    clock = Clock(1000.0)
    store = await CacheStore.open(":memory:", now=clock)
    try:
        await store.set("a", b"1", ttl=10)
        await store.set("b", b"2", ttl=100)
        clock.t = 1050.0
        removed = await store.purge()
        assert removed == 1
        assert await store.get("b") == b"2"
    finally:
        await store.close()
