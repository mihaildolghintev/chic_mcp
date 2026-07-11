from __future__ import annotations

from chic.aggregate.document import document_detail_of, document_report
from chic.moysklad.models import (
    Currency,
    Document,
    ListResponse,
    NamedRef,
    Position,
    Rate,
)


def test_document_detail_discount_reconciles() -> None:
    doc = Document(
        name="D-1",
        sum=18_000,
        payed_sum=0,
        positions=ListResponse[Position](
            rows=[
                Position(
                    assortment=NamedRef(name="Widget"),
                    quantity=2,
                    price=10_000,
                    discount=10,
                    vat=20,
                )
            ]
        ),
    )
    detail = document_detail_of(doc)
    pos = detail.positions[0]
    assert pos.price == 100.0
    # 100 * 2 * (1 - 10/100) = 180 — matches the discounted header sum.
    assert pos.total == 180.0
    assert detail.sum == 180.0


def test_document_summary_currency_from_rate() -> None:
    doc = Document(name="D-2", sum=5000, rate=Rate(currency=Currency(iso_code="EUR")))
    detail = document_detail_of(doc)
    assert detail.currency == "EUR"


def test_document_report_totals() -> None:
    docs = [
        Document(name="A", sum=10_000, payed_sum=10_000),
        Document(name="B", sum=20_000, payed_sum=5_000),
    ]
    report = document_report(docs, limit=1)
    assert report.totals.sum == 300.0
    assert report.totals.paid == 150.0
    assert report.row_count == 2
    assert report.returned == 1
    assert report.truncated is True
