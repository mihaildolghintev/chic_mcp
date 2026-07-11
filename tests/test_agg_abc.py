from __future__ import annotations

from chic.aggregate.analytics import abc, abc_report


def test_abc_classification_and_shares() -> None:
    items = abc([("A", 80.0), ("B", 15.0), ("C", 5.0)])
    by_name = {it.name: it for it in items}
    assert by_name["A"].abc_class == "A"
    assert by_name["B"].abc_class == "B"
    assert by_name["C"].abc_class == "C"
    assert by_name["A"].share == 80.0
    assert by_name["A"].cumulative_share == 80.0
    assert by_name["B"].cumulative_share == 95.0
    assert by_name["C"].cumulative_share == 100.0


def test_abc_sorted_by_value_desc() -> None:
    items = abc([("small", 5.0), ("big", 90.0), ("mid", 5.0)])
    assert [it.name for it in items] == ["big", "small", "mid"]  # stable ties


def test_abc_nonpositive_is_class_c() -> None:
    items = abc([("x", 100.0), ("zero", 0.0)])
    by_name = {it.name: it for it in items}
    assert by_name["zero"].abc_class == "C"
    assert by_name["zero"].cumulative_share == 100.0


def test_abc_report_totals_and_truncation() -> None:
    items = abc([("a", 50.0), ("b", 30.0), ("c", 15.0), ("d", 5.0)])
    report = abc_report(items, limit=2)
    assert report.totals.count == 4
    assert report.totals.value == 100.0
    assert report.row_count == 4
    assert report.returned == 2
    assert report.truncated is True
    assert report.totals.a_count + report.totals.b_count + report.totals.c_count == 4
