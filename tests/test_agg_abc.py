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


def test_abc_class_matches_displayed_cumulative_share_for_subunit_totals() -> None:
    # Sub-unit positive total (e.g. an ABC over tiny fractional profits summing to
    # 0.10): the class must use the same cum/total ratio as the displayed
    # cumulativeShare. The old cum/max(total,1) put every item under a_cut, so all
    # three came out "A" while their cumulative shares read 60/90/100%.
    items = abc([("A", 0.06), ("B", 0.03), ("C", 0.01)])  # total 0.10 < 1.0
    by_name = {it.name: it for it in items}
    assert (by_name["A"].cumulative_share, by_name["A"].abc_class) == (60.0, "A")
    assert (by_name["B"].cumulative_share, by_name["B"].abc_class) == (90.0, "B")
    assert (by_name["C"].cumulative_share, by_name["C"].abc_class) == (100.0, "C")


def test_abc_report_totals_and_truncation() -> None:
    items = abc([("a", 50.0), ("b", 30.0), ("c", 15.0), ("d", 5.0)])
    report = abc_report(items, limit=2)
    assert report.totals.count == 4
    assert report.totals.value == 100.0
    assert report.row_count == 4
    assert report.returned == 2
    assert report.truncated is True
    assert report.totals.a_count + report.totals.b_count + report.totals.c_count == 4
