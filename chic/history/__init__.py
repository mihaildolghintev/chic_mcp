"""Local snapshot history (history.db): daily stock + sales per SKU.

MoySklad has no per-SKU time series, so we accumulate our own and compute XYZ,
safety stock, trends and anomalies over it — same SQLite-first philosophy as the
rest of the app, no new services.
"""

from chic.history.snapshot import SnapshotService
from chic.history.store import DemandDay, HistoryStore

__all__ = ["DemandDay", "HistoryStore", "SnapshotService"]
