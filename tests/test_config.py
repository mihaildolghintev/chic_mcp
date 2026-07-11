from __future__ import annotations

import pytest
from chic.config import get_settings


def test_listen_addr_bare_port_defaults_host(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("LISTEN_ADDR", ":8080")
    get_settings.cache_clear()
    assert get_settings().host_port == ("0.0.0.0", 8080)


def test_listen_addr_with_host(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("LISTEN_ADDR", "127.0.0.1:9000")
    get_settings.cache_clear()
    assert get_settings().host_port == ("127.0.0.1", 9000)


def test_allowed_ids_parsing(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("ALLOWED_USER_IDS", "10, 20 ,30")
    get_settings.cache_clear()
    assert get_settings().allowed_ids == {10, 20, 30}


def test_vision_disabled_by_default() -> None:
    get_settings.cache_clear()
    assert get_settings().has_vision is False


def test_vision_enabled_with_key(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("OPENAI_API_KEY", "sk-something")
    get_settings.cache_clear()
    assert get_settings().has_vision is True


def test_tracing_toggles_on_endpoint(monkeypatch: pytest.MonkeyPatch) -> None:
    get_settings.cache_clear()
    assert get_settings().tracing_enabled is False
    monkeypatch.setenv("PHOENIX_COLLECTOR_ENDPOINT", "http://phoenix:6006")
    get_settings.cache_clear()
    assert get_settings().tracing_enabled is True
