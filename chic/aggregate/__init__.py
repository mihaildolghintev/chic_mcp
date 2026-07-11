"""Aggregate raw MoySklad responses into compact, LLM-friendly DTOs.

Monetary conversion (minor→major units) and Go-compatible rounding live here and
nowhere else. See :mod:`chic.aggregate.money`.
"""

from chic.aggregate.analytics import (
    SegmentParams,
    abc,
    abc_report,
    compare_periods,
    dead_stock,
    dead_stock_report,
    receivables_aging,
    segment_counterparties,
    segment_report,
)
from chic.aggregate.document import (
    document_detail_of,
    document_report,
    document_summaries,
    document_summary_of,
)
from chic.aggregate.money import minor_to_major, round2
from chic.aggregate.report import (
    counterparty_report,
    dashboard,
    money,
    products,
    profit_entity_report,
    profit_product_report,
    stock_report,
    turnover_report,
)

__all__ = [
    "SegmentParams",
    "abc",
    "abc_report",
    "compare_periods",
    "counterparty_report",
    "dashboard",
    "dead_stock",
    "dead_stock_report",
    "document_detail_of",
    "document_report",
    "document_summaries",
    "document_summary_of",
    "minor_to_major",
    "money",
    "products",
    "profit_entity_report",
    "profit_product_report",
    "receivables_aging",
    "round2",
    "segment_counterparties",
    "segment_report",
    "stock_report",
    "turnover_report",
]
