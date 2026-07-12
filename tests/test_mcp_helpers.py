from __future__ import annotations

import pytest
from chic.aggregate.report import product_key
from chic.mcpserver.helpers import ensure_ymd


def test_ensure_ymd_passes_empty_and_valid() -> None:
    assert ensure_ymd("", "since") == ""  # unset
    assert ensure_ymd("2026-07-13", "since") == "2026-07-13"


@pytest.mark.parametrize("bad", ["2024/01/01", "last week", "2026-13-40", "13-07-2026", "2026-7-1"])
def test_ensure_ymd_rejects_malformed(bad: str) -> None:
    # A bad date must fail loudly, not silently mis-filter the SQLite `since` compare.
    with pytest.raises(ValueError):
        ensure_ymd(bad, "since")


def test_product_key_falls_back_to_name_when_href_empty() -> None:
    assert product_key("https://ms.ru/entity/product/uuid-1", "Widget") == (
        "https://ms.ru/entity/product/uuid-1"
    )
    # Empty href ⇒ name-based key, identical on the live-report and history sides so
    # they still join (a raw "" would collapse distinct products into one bucket).
    assert product_key("", "Widget") == "name:Widget"
    assert product_key("", "Gadget") != product_key("", "Widget")
