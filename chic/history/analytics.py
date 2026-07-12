"""XYZ and ABC/XYZ analysis over the local snapshot history.

XYZ classifies demand *predictability* via the coefficient of variation (σ/μ) of a
product's daily sales: X = stable, Y = variable, Z = erratic. Combined with the
existing ABC (value) analysis it yields the 3×3 matrix that separates "hold always"
(AX) from "candidate to drop" (CZ). Statistics are stdlib only — no numpy/pandas —
which keeps the half-away/Decimal money discipline intact.
"""

from __future__ import annotations

from decimal import Decimal

from chic.aggregate.analytics import abc
from chic.aggregate.envelope import truncate
from chic.aggregate.models import (
    ABCXYZCell,
    ABCXYZItem,
    ABCXYZMatrix,
    Report,
    XYZItem,
    XYZTotals,
)
from chic.aggregate.money import dec, money_round, round2
from chic.history.olap import DemandStats
from chic.history.store import Coverage

# Standard XYZ cut-offs on the coefficient of variation.
DEFAULT_X = 0.10
DEFAULT_Y = 0.25
DEFAULT_MIN_DAYS = 3

_RECOMMENDATIONS = {
    "AX": "держать всегда, можно автопополнение",
    "AY": "держать с буфером под колебания",
    "AZ": "важный, но непредсказуемый — страховой запас и контроль",
    "BX": "стабильный середняк — умеренный запас",
    "BY": "умеренный буфер",
    "BZ": "под заказ или малый запас",
    "CX": "дешёвый, но предсказуемый — минимальный автозапас",
    "CY": "под заказ",
    "CZ": "кандидат на вывод из ассортимента",
}


def _norm_thresholds(x_threshold: float, y_threshold: float) -> tuple[float, float]:
    """Sanitize the CV cut-offs: non-negative and ordered (X band ≤ Y band).

    Mirrors :func:`chic.aggregate.analytics.abc`'s cutoff guard — a swapped or
    negative pair (e.g. x=0.25, y=0.10) would otherwise leave the Y band empty and
    silently misclassify every product, with no error raised."""
    x = max(x_threshold, 0.0)
    y = max(y_threshold, x)
    return x, y


def _xyz_class(cv: float, x_threshold: float, y_threshold: float) -> str:
    if cv <= x_threshold:
        return "X"
    if cv <= y_threshold:
        return "Y"
    return "Z"


def xyz_analysis(
    stats: list[DemandStats],
    *,
    min_days: int = DEFAULT_MIN_DAYS,
    x_threshold: float = DEFAULT_X,
    y_threshold: float = DEFAULT_Y,
    limit: int = 100,
    coverage: Coverage | None = None,
) -> Report[XYZItem, XYZTotals]:
    x_threshold, y_threshold = _norm_thresholds(x_threshold, y_threshold)
    items: list[XYZItem] = []
    unclassified = 0
    for s in stats:
        if s.days < min_days:
            unclassified += 1
            continue
        cls = _xyz_class(s.cv, x_threshold, y_threshold)
        items.append(
            XYZItem(
                name=s.name,
                code=s.code,
                days=s.days,
                mean_demand=round2(s.mean_demand),
                std_demand=round2(s.std_demand),
                cv=round(s.cv, 3),
                xyz_class=cls,
            )
        )
    items.sort(key=lambda it: it.mean_demand, reverse=True)
    totals = XYZTotals(
        count=len(items),
        x_count=sum(1 for it in items if it.xyz_class == "X"),
        y_count=sum(1 for it in items if it.xyz_class == "Y"),
        z_count=sum(1 for it in items if it.xyz_class == "Z"),
        unclassified=unclassified,
        history_days=coverage.captured_days if coverage else 0,
        history_gap_days=coverage.gap_days if coverage else 0,
        history_gaps=list(coverage.gaps) if coverage else [],
    )
    shown, full, truncated = truncate(items, limit)
    return Report[XYZItem, XYZTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


def abc_xyz_matrix(
    abc_pairs: list[tuple[str, str, Decimal]],
    stats: list[DemandStats],
    *,
    min_days: int = DEFAULT_MIN_DAYS,
    x_threshold: float = DEFAULT_X,
    y_threshold: float = DEFAULT_Y,
    limit: int = 200,
) -> ABCXYZMatrix:
    """Join period ABC (value) with history XYZ (predictability) by product id.

    ``abc_pairs`` is ``(product_key, display_name, value)``; the key (a stable
    href) is what both sides join on, the name is only for display.
    """
    x_threshold, y_threshold = _norm_thresholds(x_threshold, y_threshold)
    xyz_by_key: dict[str, tuple[str, float]] = {}
    for s in stats:
        if s.days >= min_days:
            xyz_by_key[s.product_href] = (_xyz_class(s.cv, x_threshold, y_threshold), s.cv)

    name_by_key = {key: name for key, name, _ in abc_pairs}
    abc_items = abc([(key, value) for key, _, value in abc_pairs])
    items: list[ABCXYZItem] = []
    abc_only = 0
    cell_count: dict[str, int] = {}
    cell_revenue: dict[str, Decimal] = {}
    for a in abc_items:
        xyz = xyz_by_key.get(a.name)  # a.name holds the product key here
        if xyz is None:
            abc_only += 1
            continue
        xyz_class, cv = xyz
        cell = f"{a.abc_class}{xyz_class}"
        items.append(
            ABCXYZItem(
                name=name_by_key.get(a.name, a.name),
                abc_class=a.abc_class,
                xyz_class=xyz_class,
                cell=cell,
                revenue=money_round(dec(a.value)),
                cv=round(cv, 3),
            )
        )
        cell_count[cell] = cell_count.get(cell, 0) + 1
        cell_revenue[cell] = cell_revenue.get(cell, dec(0)) + dec(a.value)

    cells = [
        ABCXYZCell(
            cell=cell,
            count=cell_count[cell],
            revenue=money_round(cell_revenue[cell]),
            recommendation=_RECOMMENDATIONS.get(cell, ""),
        )
        for cell in sorted(cell_count)
    ]
    items.sort(key=lambda it: it.revenue, reverse=True)
    shown, full, truncated = truncate(items, limit)
    return ABCXYZMatrix(
        classified=len(items),
        abc_only=abc_only,
        cells=cells,
        items=shown,
        item_count=full,
        items_truncated=truncated,
    )
