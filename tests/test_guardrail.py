from __future__ import annotations

from decimal import Decimal

from chic.agent.guardrail import (
    check_answer,
    collect_known_numbers,
    extract_numbers,
)

NBSP = " "


def _values(text: str) -> list[Decimal]:
    return [n.value for n in extract_numbers(text)]


def test_extract_grouped_space_and_nbsp() -> None:
    assert Decimal("847320") in _values("Выручка 847 320 MDL")
    assert Decimal("1234567") in _values(f"Итого 1{NBSP}234{NBSP}567")


def test_extract_decimal_comma_and_percent() -> None:
    nums = extract_numbers("маржа 12,5% при цене 99.90")
    by_val = {n.value: n for n in nums}
    assert by_val[Decimal("12.5")].is_percent is True
    assert by_val[Decimal("99.90")].fractional is True


def test_extract_negative() -> None:
    assert Decimal("-1200.50") in _values("Прибыль изменилась на -1 200,50")


def test_extract_skips_code_dates_urls() -> None:
    text = "Артикул `ART-100500`, дата 2026-07-12, ссылка https://ms.ru/x/9988. Продажи 5 000."
    vals = _values(text)
    assert Decimal("5000") in vals
    assert Decimal("100500") not in vals  # inside code span
    assert Decimal("9988") not in vals  # inside url


def test_extract_marks_grouping_and_fraction_flags() -> None:
    (n,) = extract_numbers("ровно 2 500")
    assert n.grouped is True and n.fractional is False


def test_collect_known_from_nested_json() -> None:
    payload = '{"totals":{"revenue":847320.0,"marginPct":12.5},"rows":[{"profit":1000.0}]}'
    known = collect_known_numbers([payload])
    assert {Decimal("847320.0"), Decimal("12.5"), Decimal("1000.0")} <= known


def test_collect_known_excludes_bools_and_wraps_truncated() -> None:
    known = collect_known_numbers(['{"applicable":true,"sum":500.0'], extra="про 300 штук")
    assert Decimal("500.0") in known  # recovered from unparseable (truncated) json
    assert Decimal("300") in known  # from the user's own message
    assert Decimal("1") not in known  # `true` must not leak in as 1


def test_check_answer_grounded_within_rounding() -> None:
    known = {Decimal("847320.00"), Decimal("12.5")}
    # exact, off-by-one-cent, and the percentage all trace back.
    report = check_answer("Выручка 847 320, маржа 12,5%", known)
    assert report.unexplained == []
    assert report.checked == 2


def test_check_answer_flags_fabricated_number() -> None:
    known = {Decimal("847320.00")}
    report = check_answer("Выручка была 900 000", known)
    assert report.unexplained == ["900 000"]
    assert report.grounded == 0


def test_check_answer_flags_money_scale_transcription_slip() -> None:
    # The motivating case: a few-digit slip on a money figure (847320 → 843720) is
    # only 0.4% off, but must still be flagged — the tolerance can't be blind to it.
    known = {Decimal("847320.00")}
    report = check_answer("Выручка составила 843 720", known)
    assert report.unexplained == ["843 720"]
    assert report.grounded == 0


def test_check_answer_ignores_small_counts_and_years() -> None:
    known: set[Decimal] = set()
    report = check_answer("В 2026 году показать первые 5 позиций из 3 складов", known)
    # 2026 (year), 5 and 3 (small counts) are not significant → nothing to explain.
    assert report.checked == 0
    assert report.unexplained == []
