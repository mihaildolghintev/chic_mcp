from __future__ import annotations

from decimal import Decimal

from chic.aggregate.money import (
    dec,
    margin_pct,
    minor_to_major,
    money_round,
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


def test_dec_uses_shortest_repr() -> None:
    # Decimal(str(x)) preserves what the float meant, not its binary expansion.
    assert dec(0.1) == Decimal("0.1")
    assert dec(123.45) == Decimal("123.45")
    assert dec(Decimal("7.00")) == Decimal("7.00")


def test_minor_to_major_returns_decimal() -> None:
    assert minor_to_major(12345) == Decimal("123.45")
    assert minor_to_major(150) == Decimal("1.50")
    assert minor_to_major(0) == Decimal("0.00")
    # Result is a 2-dp Decimal, not a float — no binary drift downstream.
    assert isinstance(minor_to_major(12345), Decimal)
    assert str(minor_to_major(12345)) == "123.45"


def test_money_round_halves_away_from_zero() -> None:
    assert money_round(Decimal("2.555")) == Decimal("2.56")
    assert money_round(Decimal("2.545")) == Decimal("2.55")  # not banker's 2.54
    assert money_round(Decimal("-2.555")) == Decimal("-2.56")
    # The classic float trap: 0.145 * 100 == 14.4999… under binary floats, so the
    # old `round2` path rounded this DOWN to 0.14. Decimal rounds the true half up.
    assert money_round(Decimal("0.145")) == Decimal("0.15")
    assert round2(0.145) == 0.14  # the legacy float behaviour, for contrast
    assert money_round(dec(0.1) + dec(0.2)) == Decimal("0.30")  # 0.30, not 0.30000000004


def test_margin_pct() -> None:
    assert margin_pct(30.0, 100.0) == 30.0
    assert margin_pct(Decimal("30"), Decimal("100")) == 30.0
    assert margin_pct(5.0, 0.0) == 0.0  # guard against divide-by-zero


def test_pct_change() -> None:
    assert pct_change(100.0, 120.0) == 20.0
    assert pct_change(0.0, 0.0) == 0.0
    assert pct_change(0.0, 50.0) == 100.0
    assert pct_change(200.0, 100.0) == -50.0
    assert pct_change(Decimal("200"), Decimal("100")) == -50.0


def test_round2_non_money() -> None:
    assert round2(123.456) == 123.46
    assert round2(2.5555) == 2.56
    assert round2(1.0 / 3.0) == 0.33
