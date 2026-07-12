"""Supported MoySklad document types.

Single source of truth: order drives the enum shown to MCP clients, and
``has_currency`` gates the ``rate.currency`` expand (warehouse ops reject it).
"""

from __future__ import annotations

from enum import StrEnum


class DocumentType(StrEnum):
    DEMAND = "demand"  # отгрузка (продажа)
    CUSTOMER_ORDER = "customerorder"  # заказ покупателя
    SUPPLY = "supply"  # приёмка (закупка)
    PURCHASE_ORDER = "purchaseorder"  # заказ поставщику
    INTERNAL_ORDER = "internalorder"  # внутренний заказ
    INVOICE_OUT = "invoiceout"  # счёт покупателю
    INVOICE_IN = "invoicein"  # счёт поставщика
    SALES_RETURN = "salesreturn"  # возврат покупателя
    PURCHASE_RETURN = "purchasereturn"  # возврат поставщику
    PAYMENT_IN = "paymentin"  # входящий платёж (банк)
    PAYMENT_OUT = "paymentout"  # исходящий платёж (банк)
    CASH_IN = "cashin"  # приходный ордер (касса)
    CASH_OUT = "cashout"  # расходный ордер (касса)
    COMMISSION_REPORT_IN = "commissionreportin"  # отчёт комиссионера (полученный)
    COMMISSION_REPORT_OUT = "commissionreportout"  # отчёт комиссионеру (выданный)
    FACTURE_OUT = "factureout"  # счёт-фактура выданный
    FACTURE_IN = "facturein"  # счёт-фактура полученный
    COUNTERPARTY_ADJUSTMENT = "counterpartyadjustment"  # корректировка взаиморасчётов
    PREPAYMENT = "prepayment"  # предоплата (розница)
    PREPAYMENT_RETURN = "prepaymentreturn"  # возврат предоплаты (розница)
    RETAIL_DEMAND = "retaildemand"  # розничная продажа
    RETAIL_SALES_RETURN = "retailsalesreturn"  # розничный возврат
    RETAIL_SHIFT = "retailshift"  # розничная смена
    RETAIL_DRAWER_CASH_IN = "retaildrawercashin"  # внесение в кассу смены
    RETAIL_DRAWER_CASH_OUT = "retaildrawercashout"  # выплата из кассы смены
    MOVE = "move"  # перемещение между складами
    INVENTORY = "inventory"  # инвентаризация
    LOSS = "loss"  # списание
    ENTER = "enter"  # оприходование
    PROCESSING = "processing"  # техоперация


# Display order + whether the type carries a currency/rate. Commercial docs do;
# warehouse operations are always in the base currency and reject rate.currency.
_DOCUMENT_TYPES: tuple[tuple[DocumentType, bool], ...] = (
    (DocumentType.DEMAND, True),
    (DocumentType.CUSTOMER_ORDER, True),
    (DocumentType.SUPPLY, True),
    (DocumentType.PURCHASE_ORDER, True),
    (DocumentType.INTERNAL_ORDER, False),
    (DocumentType.INVOICE_OUT, True),
    (DocumentType.INVOICE_IN, True),
    (DocumentType.SALES_RETURN, True),
    (DocumentType.PURCHASE_RETURN, True),
    (DocumentType.PAYMENT_IN, True),
    (DocumentType.PAYMENT_OUT, True),
    (DocumentType.CASH_IN, True),
    (DocumentType.CASH_OUT, True),
    (DocumentType.COMMISSION_REPORT_IN, True),
    (DocumentType.COMMISSION_REPORT_OUT, True),
    (DocumentType.FACTURE_OUT, False),
    (DocumentType.FACTURE_IN, False),
    (DocumentType.COUNTERPARTY_ADJUSTMENT, True),
    (DocumentType.PREPAYMENT, True),
    (DocumentType.PREPAYMENT_RETURN, True),
    (DocumentType.RETAIL_DEMAND, True),
    (DocumentType.RETAIL_SALES_RETURN, True),
    (DocumentType.RETAIL_SHIFT, True),
    (DocumentType.RETAIL_DRAWER_CASH_IN, True),
    (DocumentType.RETAIL_DRAWER_CASH_OUT, True),
    (DocumentType.MOVE, False),
    (DocumentType.INVENTORY, False),
    (DocumentType.LOSS, False),
    (DocumentType.ENTER, False),
    (DocumentType.PROCESSING, False),
)

_HAS_CURRENCY: dict[str, bool] = {t.value: has for t, has in _DOCUMENT_TYPES}


def has_currency(doc_type: str) -> bool:
    """Whether a document type carries a currency/rate (commercial docs only)."""
    return _HAS_CURRENCY.get(doc_type, False)


def valid_document_type(s: str) -> bool:
    return s in _HAS_CURRENCY


def document_type_strings() -> list[str]:
    """Every supported type in display order (for a JSON-schema enum)."""
    return [t.value for t, _ in _DOCUMENT_TYPES]
