from __future__ import annotations

from chic.moysklad.documents import (
    document_type_strings,
    has_currency,
    valid_document_type,
)


def test_has_currency_commercial_vs_warehouse() -> None:
    assert has_currency("demand") is True
    assert has_currency("invoiceout") is True
    assert has_currency("retaildemand") is True
    assert has_currency("commissionreportin") is True
    # Warehouse operations reject a rate.currency expand.
    assert has_currency("move") is False
    assert has_currency("inventory") is False
    assert has_currency("loss") is False
    assert has_currency("enter") is False
    assert has_currency("processing") is False
    assert has_currency("internalorder") is False
    assert has_currency("unknown") is False


def test_valid_document_type() -> None:
    assert valid_document_type("demand") is True
    assert valid_document_type("nope") is False


def test_document_type_strings_order_and_count() -> None:
    types = document_type_strings()
    assert len(types) == 30
    assert types[0] == "demand"
    assert types[-1] == "processing"
    # Retail and cash-order types are now covered.
    assert "retaildemand" in types
    assert "cashin" in types
    assert "commissionreportout" in types
