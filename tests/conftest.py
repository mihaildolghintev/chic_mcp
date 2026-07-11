from __future__ import annotations

from collections.abc import Iterator

import pytest
from chic.config import get_settings


@pytest.fixture(autouse=True)
def _env(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """Provide the required secrets so ``Settings()`` validates in tests."""
    monkeypatch.setenv("TELEGRAM_BOT_TOKEN", "test-token")
    monkeypatch.setenv("TELEGRAM_WEBHOOK_SECRET", "test-secret")
    monkeypatch.setenv("ALLOWED_USER_IDS", "1,2,3")
    monkeypatch.setenv("MOYSKLAD_TOKEN", "test-moysklad")
    monkeypatch.setenv("DEEPSEEK_API_KEY", "test-deepseek")
    monkeypatch.setenv("PUBLIC_BASE_URL", "https://example.test")
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()
