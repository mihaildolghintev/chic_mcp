from __future__ import annotations

from chic.aggregate.money import (
    margin_pct,
    minor_to_major,
    pct_change,
    round2,
    round_half_away,
)


def test_round_half_away_matches_go_math_round() -> None:
    # Half away from zero (NOT banker's rounding).
    assert round_half_away(0.5) == 1
    assert round_half_away(1.5) == 2
    assert round_half_away(2.5) == 3  # banker's rounding would give 2
    assert round_half_away(-0.5) == -1
    assert round_half_away(-2.5) == -3


def test_minor_to_major() -> None:
    assert minor_to_major(12345) == 123.45
    assert minor_to_major(150) == 1.5
    assert minor_to_major(0) == 0.0


def test_margin_pct() -> None:
    assert margin_pct(30.0, 100.0) == 30.0
    assert margin_pct(5.0, 0.0) == 0.0  # guard against divide-by-zero


def test_pct_change() -> None:
    assert pct_change(100.0, 120.0) == 20.0
    assert pct_change(0.0, 0.0) == 0.0
    assert pct_change(0.0, 50.0) == 100.0
    assert pct_change(200.0, 100.0) == -50.0


def test_round2() -> None:
    assert round2(123.456) == 123.46
    assert round2(2.5555) == 2.56
    assert round2(1.0 / 3.0) == 0.33
