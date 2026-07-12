"""DuckDB OLAP over the snapshot history: dense demand series → per-SKU statistics.

The forecasting inputs (mean/σ/CV of daily demand) need a *dense* series — a day a
product sold nothing is a real 0, from its first sale onward. Expressing that
zero-fill and the aggregation as a single DuckDB query offloads the heavy work to
a vectorized engine instead of Python loops, and scales as history grows.

DuckDB runs in-memory and is fed the rows read from ``history.db`` (no
``sqlite_scanner`` extension, so nothing is downloaded at runtime — hermetic in
tests, offline-safe on deploy). Bound the input with ``since`` as history grows.
"""

from __future__ import annotations

import dataclasses

import duckdb

from chic.history.store import DemandDay

# One query: build the per-product date spine from its first sale, left-join the
# actual sales (missing days ⇒ 0), then aggregate. stddev_pop == statistics.pstdev.
_STATS_SQL = """
WITH all_dates AS (SELECT DISTINCT snapshot_date AS d FROM demand),
first_seen AS (
    SELECT product_href,
           min(snapshot_date) AS first_date,
           arg_max(name, snapshot_date) AS name,
           arg_max(code, snapshot_date) AS code
    FROM demand
    GROUP BY product_href
),
spine AS (
    SELECT f.product_href, f.name, f.code, a.d AS snapshot_date
    FROM first_seen f
    JOIN all_dates a ON a.d >= f.first_date
),
filled AS (
    SELECT s.product_href, s.name, s.code, s.snapshot_date,
           COALESCE(dm.sell_quantity, 0.0) AS qty
    FROM spine s
    LEFT JOIN demand dm
        ON dm.product_href = s.product_href AND dm.snapshot_date = s.snapshot_date
)
SELECT product_href,
       any_value(name) AS name,
       any_value(code) AS code,
       count(*) AS days,
       avg(qty) AS mean_demand,
       stddev_pop(qty) AS std_demand
FROM filled
GROUP BY product_href
"""


@dataclasses.dataclass(frozen=True)
class DemandStats:
    product_href: str
    name: str
    code: str
    days: int
    mean_demand: float
    std_demand: float

    @property
    def cv(self) -> float:
        return self.std_demand / self.mean_demand if self.mean_demand > 0 else 0.0


def demand_stats(rows: list[DemandDay]) -> list[DemandStats]:
    """Compute per-product demand mean/σ over the zero-filled daily series."""
    if not rows:
        return []
    con = duckdb.connect()
    try:
        con.execute(
            "CREATE TABLE demand("
            "snapshot_date VARCHAR, product_href VARCHAR, name VARCHAR, "
            "code VARCHAR, sell_quantity DOUBLE)"
        )
        con.executemany(
            "INSERT INTO demand VALUES (?, ?, ?, ?, ?)",
            [(r.snapshot_date, r.product_href, r.name, r.code, r.sell_quantity) for r in rows],
        )
        result = con.execute(_STATS_SQL).fetchall()
    finally:
        con.close()

    return [
        DemandStats(
            product_href=href,
            name=name,
            code=code,
            days=int(days),
            mean_demand=float(mean or 0.0),
            std_demand=float(std or 0.0),
        )
        for href, name, code, days, mean, std in result
    ]
