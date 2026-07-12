"""SQLAlchemy tables for the local snapshot store (history.db).

MoySklad exposes no per-SKU time series, so we accumulate our own: one daily row
per product for stock (point-in-time) and for sales (a complete day's demand).
That local history is what unlocks XYZ, safety stock, trends and anomalies without
hammering the API.

Money is stored as raw **minor units** (float, as the API returns them) — exact for
the magnitudes involved and converted through :mod:`chic.aggregate.money` only at
read time, so no binary-float drift leaks into the analytics.
"""

from __future__ import annotations

from sqlalchemy import Float, Integer, String, Text
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column


class Base(DeclarativeBase):
    pass


class StockSnapshotRow(Base):
    """On-hand stock for one product as of ``snapshot_date`` (point-in-time)."""

    __tablename__ = "stock_snapshot"

    snapshot_date: Mapped[str] = mapped_column(String, primary_key=True)  # YYYY-MM-DD
    product_href: Mapped[str] = mapped_column(String, primary_key=True)
    name: Mapped[str] = mapped_column(Text, nullable=False, default="")
    code: Mapped[str] = mapped_column(String, nullable=False, default="")
    stock: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    reserve: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    in_transit: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    price_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)  # cost
    sale_price_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    stock_days: Mapped[int] = mapped_column(Integer, nullable=False, default=0)


class SalesSnapshotRow(Base):
    """One product's demand over a single complete day ``snapshot_date``."""

    __tablename__ = "sales_snapshot"

    snapshot_date: Mapped[str] = mapped_column(String, primary_key=True)  # YYYY-MM-DD
    product_href: Mapped[str] = mapped_column(String, primary_key=True)
    name: Mapped[str] = mapped_column(Text, nullable=False, default="")
    code: Mapped[str] = mapped_column(String, nullable=False, default="")
    sell_quantity: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    sell_sum_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    sell_cost_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    profit_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    return_quantity: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)
    return_sum_minor: Mapped[float] = mapped_column(Float, nullable=False, default=0.0)


class SalesCaptureRow(Base):
    """Marker that one day's sales snapshot was *processed* — even if it had zero
    sales and thus wrote no ``SalesSnapshotRow``. This is what lets a genuine outage
    gap (day never captured) be told apart from a legitimately closed/zero day."""

    __tablename__ = "sales_capture"

    snapshot_date: Mapped[str] = mapped_column(String, primary_key=True)  # YYYY-MM-DD
    rows: Mapped[int] = mapped_column(Integer, nullable=False, default=0)  # sales rows written
