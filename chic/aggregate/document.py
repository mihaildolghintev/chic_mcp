"""Document summaries and expanded detail (minor→major units)."""

from __future__ import annotations

from typing import Any

from chic.aggregate.models import (
    DocumentDetail,
    DocumentSummary,
    DocumentTotals,
    PositionRow,
    Report,
)
from chic.aggregate.money import minor_to_major, round2
from chic.aggregate.report import _truncate
from chic.moysklad.models import Document


def _attr_value(value: Any) -> Any:
    """Flatten a custom-attribute value to a display scalar.

    Custom-entity / linked attributes arrive as objects carrying their own
    ``name``; simple attributes are scalars already.
    """
    if isinstance(value, dict):
        return value.get("name") or value.get("value") or ""
    return value


def _attributes(d: Document) -> dict[str, Any]:
    return {a.name: _attr_value(a.value) for a in d.attributes if a.name}


def document_summary_of(d: Document) -> DocumentSummary:
    return DocumentSummary(
        id=d.id,
        name=d.name,
        moment=d.moment,
        sum=minor_to_major(d.sum),
        paid=minor_to_major(d.payed_sum),
        applicable=d.applicable,
        currency=(
            d.rate.currency.iso_code if d.rate is not None and d.rate.currency is not None else ""
        ),
        counterparty=d.agent.name if d.agent is not None else "",
        state=d.state.name if d.state is not None else "",
        store=d.store.name if d.store is not None else "",
        channel=d.sales_channel.name if d.sales_channel is not None else "",
    )


def document_summaries(docs: list[Document]) -> list[DocumentSummary]:
    return [document_summary_of(d) for d in docs]


def document_report(docs: list[Document], limit: int) -> Report[DocumentSummary, DocumentTotals]:
    rows = document_summaries(docs)
    totals = DocumentTotals(
        sum=round2(sum(r.sum for r in rows)),
        paid=round2(sum(r.paid for r in rows)),
    )
    shown, full, truncated = _truncate(rows, limit)
    return Report[DocumentSummary, DocumentTotals](
        totals=totals, row_count=full, returned=len(shown), truncated=truncated, rows=shown
    )


def document_detail_of(d: Document) -> DocumentDetail:
    base = document_summary_of(d)
    positions: list[PositionRow] = []
    if d.positions is not None:
        for p in d.positions.rows:
            price = minor_to_major(p.price)
            # MoySklad's document Sum is already net of line discounts, so the
            # line total must subtract the discount too to reconcile with the header.
            total = price * p.quantity * (1 - p.discount / 100)
            positions.append(
                PositionRow(
                    name=p.assortment.name,
                    code=p.assortment.code,
                    quantity=p.quantity,
                    price=price,
                    discount=p.discount,
                    vat=p.vat,
                    total=round2(total),
                )
            )
    return DocumentDetail(
        **base.model_dump(),
        description=d.description,
        vat_sum=minor_to_major(d.vat_sum),
        payment_due_date=d.payment_planned_moment,
        positions=positions,
        attributes=_attributes(d),
    )
