from __future__ import annotations

from decimal import Decimal

import pytest
from chic.history.analytics import abc_xyz_matrix, xyz_analysis
from chic.history.olap import DemandStats, demand_stats
from chic.history.store import DemandDay


def _demand(date: str, href: str, name: str, qty: float) -> DemandDay:
    return DemandDay(
        snapshot_date=date,
        product_href=href,
        name=name,
        code="",
        sell_quantity=qty,
        sell_sum_minor=qty * 1000,
        sell_cost_minor=qty * 600,
        profit_minor=qty * 400,
    )


def test_demand_stats_zero_fills_from_first_sale() -> None:
    rows = [
        _demand("2026-07-01", "h1", "A", 5),
        _demand("2026-07-03", "h1", "A", 5),  # sold nothing on the 2nd
        _demand("2026-07-02", "h2", "B", 2),  # first seen on the 2nd
    ]
    stats = {s.product_href: s for s in demand_stats(rows)}

    # h1: dense [5, 0, 5] from 07-01; h2: dense [2, 0] from its first sale.
    assert stats["h1"].days == 3
    assert stats["h1"].mean_demand == pytest.approx(10 / 3)
    assert stats["h2"].days == 2
    assert stats["h2"].mean_demand == pytest.approx(1.0)
    assert stats["h2"].std_demand == pytest.approx(1.0)


def _stat(name: str, days: int, mean: float, std: float) -> DemandStats:
    return DemandStats(
        product_href=name, name=name, code="", days=days, mean_demand=mean, std_demand=std
    )


def test_xyz_classifies_by_variability() -> None:
    stats = [
        _stat("Stable", 4, 4.0, 0.0),  # cv 0 → X
        _stat("Erratic", 4, 3.75, 4.146),  # cv ≈ 1.1 → Z
        _stat("New", 1, 3.0, 0.0),  # too few days → unclassified
    ]
    report = xyz_analysis(stats, min_days=3)

    by_name = {r.name: r for r in report.rows}
    assert by_name["Stable"].xyz_class == "X"
    assert by_name["Stable"].cv == 0.0
    assert by_name["Erratic"].xyz_class == "Z"
    assert report.totals.unclassified == 1  # "New"
    assert report.totals.count == 2


def test_xyz_normalizes_swapped_thresholds() -> None:
    # Swapped cut-offs (x > y) must not leave an unreachable band producing garbage:
    # normalization forces x ≤ y (here x=y=0.25), so classification stays deterministic
    # and total — cv 0.2 ≤ 0.25 → X, cv 3.0 → Z — instead of silently misclassifying.
    stats = [_stat("Mid", 4, 5.0, 1.0), _stat("Wild", 4, 1.0, 3.0)]  # cv 0.2 and 3.0
    rep = xyz_analysis(stats, min_days=3, x_threshold=0.25, y_threshold=0.10)
    by = {r.name: r.xyz_class for r in rep.rows}
    assert by == {"Mid": "X", "Wild": "Z"}


def test_abc_xyz_matrix_joins_value_and_predictability() -> None:
    # (product_key, display_name, value) — key is the stable join id.
    pairs: list[tuple[str, str, Decimal]] = [
        ("Stable", "Stable", Decimal("100000")),
        ("Erratic", "Erratic", Decimal("50000")),
        ("NoHistory", "NoHistory", Decimal("10000")),
    ]
    stats = [_stat("Stable", 4, 4.0, 0.0), _stat("Erratic", 4, 3.75, 4.146)]

    matrix = abc_xyz_matrix(pairs, stats, min_days=3)

    assert matrix.classified == 2
    assert matrix.abc_only == 1  # NoHistory has revenue but no XYZ history
    assert "AX" in {c.cell for c in matrix.cells}  # Stable: top value + stable demand
    by_name = {i.name: i for i in matrix.items}
    assert by_name["Stable"].cell == "AX"
    assert by_name["Erratic"].xyz_class == "Z"
