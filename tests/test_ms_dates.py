from __future__ import annotations

import pytest
from chic.moysklad.dates import normalize_moment, normalize_moment_end


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("", ""),
        ("  ", ""),
        ("2026-07-01", "2026-07-01 00:00:00"),
        ("2026-07-01 12:30:00", "2026-07-01 12:30:00"),
        ("2026-07-01T12:30:45", "2026-07-01 12:30:45"),
        ("2026-07-01T12:30:45Z", "2026-07-01 12:30:45"),
    ],
)
def test_normalize_moment(raw: str, expected: str) -> None:
    assert normalize_moment(raw) == expected


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("", ""),
        ("2026-07-10", "2026-07-10 23:59:59"),  # bare end date ⇒ end of day
        ("2026-07-10 08:00:00", "2026-07-10 08:00:00"),
        ("2026-07-10T08:00:00", "2026-07-10 08:00:00"),
    ],
)
def test_normalize_moment_end(raw: str, expected: str) -> None:
    assert normalize_moment_end(raw) == expected
