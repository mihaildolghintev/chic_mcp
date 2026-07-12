from __future__ import annotations

from decimal import Decimal

from chic.aggregate.bridge import profit_bridge
from chic.aggregate.models import ProfitProductLine


def line(name: str, qty: float, revenue: str, cost: str) -> ProfitProductLine:
    return ProfitProductLine(
        id=name,  # tests use the name as the stable id
        name=name,
        sell_quantity=qty,
        revenue=Decimal(revenue),
        cost=Decimal(cost),
        return_sum=Decimal(0),
        profit=Decimal(revenue) - Decimal(cost),
        margin_pct=0.0,
    )


def test_profit_bridge_price_volume_new_discontinued() -> None:
    # A: Widget (q10, gross 400), Gadget (q5, gross 200) → profit 600
    # B: Widget (q12, price +10/unit, gross 600), Gizmo new (q4, gross 200) → profit 800
    # Gadget is discontinued.
    a = [line("Widget", 10, "1000", "600"), line("Gadget", 5, "500", "300")]
    b = [line("Widget", 12, "1320", "720"), line("Gizmo", 4, "400", "200")]

    br = profit_bridge(a, b, top_n=10)

    assert br.profit_a == Decimal("600.00")
    assert br.profit_b == Decimal("800.00")
    assert br.delta == Decimal("200.00")

    assert br.price_effect == Decimal("120.00")  # Widget price +10 × 12 units
    assert br.cost_effect == Decimal("0.00")  # unit cost unchanged
    assert br.volume_effect == Decimal("80.00")  # +2 Widgets at 40/unit baseline margin
    assert br.mix_effect == Decimal("0.00")  # single common product ⇒ no mix shift
    assert br.new_products_effect == Decimal("200.00")  # Gizmo
    assert br.discontinued_effect == Decimal("-200.00")  # Gadget
    assert br.rounding == Decimal("0.00")

    assert (br.common_count, br.new_count, br.discontinued_count) == (1, 1, 1)


def test_profit_bridge_driver_name_is_display_name_not_href() -> None:
    # Products join on the href id, but the driver label must be the display name.
    def named(href: str, display: str, qty: float, rev: str, cost: str) -> ProfitProductLine:
        return ProfitProductLine(
            id=href,
            name=display,
            sell_quantity=qty,
            revenue=Decimal(rev),
            cost=Decimal(cost),
            return_sum=Decimal(0),
            profit=Decimal(rev) - Decimal(cost),
            margin_pct=0.0,
        )

    href = "https://ms.ru/entity/product/abc-123"
    a = [named(href, "Виджет", 10, "1000", "600"), named("h2", "Старьё", 3, "300", "100")]
    b = [named(href, "Виджет", 12, "1320", "720"), named("h3", "Новинка", 4, "400", "200")]

    br = profit_bridge(a, b, top_n=10)
    names = {d.name for d in br.top_drivers}
    assert names == {"Виджет", "Старьё", "Новинка"}  # display names, never the href
    assert not any(d.name.startswith("http") for d in br.top_drivers)


def test_profit_bridge_cost_driven_decline() -> None:
    # Same units and price, unit cost rises 60 → 70: the whole −100 is a cost effect.
    a = [line("Widget", 10, "1000", "600")]
    b = [line("Widget", 10, "1000", "700")]

    br = profit_bridge(a, b, top_n=10)

    assert br.delta == Decimal("-100.00")
    assert br.cost_effect == Decimal("-100.00")
    assert br.price_effect == Decimal("0.00")
    assert br.volume_effect == Decimal("0.00")
    assert br.mix_effect == Decimal("0.00")

    driver = br.top_drivers[0]
    assert driver.name == "Widget"
    assert driver.kind == "common"
    assert driver.cost_effect == Decimal("-100.00")
    assert driver.delta == Decimal("-100.00")


def test_profit_bridge_effects_reconcile_with_messy_numbers() -> None:
    # Non-round unit prices force Decimal division; the rounded effects + rounding
    # residual must still sum back to the delta exactly (the grounding guarantee).
    a = [line("A", 3, "100", "33"), line("B", 7, "250", "111")]
    b = [line("A", 4, "140", "50"), line("C", 2, "90", "40")]

    br = profit_bridge(a, b, top_n=10)

    total = (
        br.price_effect
        + br.cost_effect
        + br.volume_effect
        + br.mix_effect
        + br.new_products_effect
        + br.discontinued_effect
        + br.rounding
    )
    assert total == br.delta


def test_profit_bridge_top_n_limits_drivers() -> None:
    a = [line(f"P{i}", 1, str(100 + i), "50") for i in range(20)]
    b = [line(f"P{i}", 1, str(200 + i), "50") for i in range(20)]

    br = profit_bridge(a, b, top_n=5)
    assert len(br.top_drivers) == 5
    # Sorted by absolute delta descending.
    deltas = [abs(d.delta) for d in br.top_drivers]
    assert deltas == sorted(deltas, reverse=True)
