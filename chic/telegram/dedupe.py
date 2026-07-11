"""Update deduplication — Telegram re-delivers updates it doubts we received."""

from __future__ import annotations


class Dedupe:
    """Remembers the last n update ids in a fixed-size ring (bounded memory)."""

    def __init__(self, n: int = 1024) -> None:
        self._seen: set[int] = set()
        self._ring: list[int] = [0] * n
        self._next = 0

    def first_seen(self, update_id: int) -> bool:
        if update_id in self._seen:
            return False
        old = self._ring[self._next]
        if old != 0:
            self._seen.discard(old)
        self._ring[self._next] = update_id
        self._next = (self._next + 1) % len(self._ring)
        self._seen.add(update_id)
        return True
