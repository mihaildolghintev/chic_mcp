"""Price / Volume / Mix / Cost bridge for gross-profit change between periods.

Answers "why did profit change" by decomposing the gross-profit delta into
factors the owner can act on, rather than reporting a single percentage. Gross
profit is defined here as ``revenue - cost`` per product so the decomposition is
algebraically exact; the effects always reconcile to the total delta.

All money arithmetic is Decimal (see :mod:`chic.aggregate.money`). The per-product
volume/mix effect absorbs the Decimal-division residual, which keeps the identity
``price + cost + volume + mix + new + discontinued == delta`` exact to the cent —
this is the invariant the LLM's narrative is allowed to trust.
"""

from __future__ import annotations

import logging
from decimal import Decimal

from chic.aggregate.models import BridgeDriver, ProfitBridge, ProfitProductLine
from chic.aggregate.money import dec, money_round

logger = logging.getLogger(__name__)


def _by_id(lines: list[ProfitProductLine]) -> dict[str, ProfitProductLine]:
    """Index sold products by stable id (qty > 0), folding duplicates together.

    Keying on the product id (href) rather than the display name means two distinct
    products that share a name are not silently merged across the two periods.
    """
    out: dict[str, ProfitProductLine] = {}
    for ln in lines:
        if ln.sell_quantity <= 0:
            continue
        prev = out.get(ln.id)
        if prev is None:
            out[ln.id] = ln
        else:  # same id twice in one period → combine
            out[ln.id] = ProfitProductLine(
                id=ln.id,
                name=ln.name,
                sell_quantity=prev.sell_quantity + ln.sell_quantity,
                revenue=prev.revenue + ln.revenue,
                cost=prev.cost + ln.cost,
                return_sum=prev.return_sum + ln.return_sum,
                profit=prev.profit + ln.profit,
                margin_pct=0.0,
            )
    return out


def _gross(ln: ProfitProductLine) -> Decimal:
    return ln.revenue - ln.cost


def profit_bridge(
    a_lines: list[ProfitProductLine], b_lines: list[ProfitProductLine], top_n: int
) -> ProfitBridge:
    a = _by_id(a_lines)
    b = _by_id(b_lines)
    common = a.keys() & b.keys()
    a_only = a.keys() - b.keys()
    b_only = b.keys() - a.keys()

    price = cost = volmix = dec(0)
    qa_common = qb_common = profit_a_common = dec(0)
    drivers: list[BridgeDriver] = []

    for key in common:
        la, lb = a[key], b[key]
        qa, qb = dec(la.sell_quantity), dec(lb.sell_quantity)
        # Unit price / cost per period; the baseline margin per unit feeds volume.
        pa, ca = la.revenue / qa, la.cost / qa
        pb, cb = lb.revenue / qb, lb.cost / qb

        profit_delta = _gross(lb) - _gross(la)
        price_i = qb * (pb - pa)
        cost_i = -qb * (cb - ca)
        # Volume+mix absorbs the division residual so price+cost+qty == delta exactly.
        qty_i = profit_delta - price_i - cost_i

        price += price_i
        cost += cost_i
        volmix += qty_i
        qa_common += qa
        qb_common += qb
        profit_a_common += _gross(la)

        drivers.append(
            BridgeDriver(
                name=la.name,  # display name, not the href key we join on
                kind="common",
                qty_a=la.sell_quantity,
                qty_b=lb.sell_quantity,
                profit_a=money_round(_gross(la)),
                profit_b=money_round(_gross(lb)),
                delta=money_round(profit_delta),
                price_effect=money_round(price_i),
                cost_effect=money_round(cost_i),
                qty_effect=money_round(qty_i),
            )
        )

    # Pure volume = total-quantity change valued at the baseline average unit margin;
    # mix is the remainder (composition shift at baseline margins).
    volume = (qb_common - qa_common) * (profit_a_common / qa_common) if qa_common > 0 else dec(0)
    mix = volmix - volume

    new_eff = dec(0)
    for key in b_only:
        g = _gross(b[key])
        new_eff += g
        drivers.append(_absent_driver(b[key].name, "new", b[key], dec(0), g, g))
    disc_eff = dec(0)
    for key in a_only:
        g = _gross(a[key])
        disc_eff -= g
        drivers.append(_absent_driver(a[key].name, "discontinued", a[key], g, dec(0), -g))

    profit_a = sum((_gross(a[n]) for n in a), dec(0))
    profit_b = sum((_gross(b[n]) for n in b), dec(0))
    delta = profit_b - profit_a

    # Algebraic invariant: the six effects reconcile to the delta. It holds by
    # construction; the tolerance only absorbs Decimal's 28-digit division ulps
    # (well under a cent), and is the guarantee the LLM's narrative may trust.
    # Not a bare `assert` (those vanish under `python -O`): log if the invariant
    # is ever broken by a future change instead of silently shipping a bad bridge.
    residual = price + cost + volume + mix + new_eff + disc_eff - delta
    if abs(residual) >= dec("0.01"):
        logger.error("bridge reconciliation off by %s (should be ~0)", residual)

    effects = [
        money_round(price),
        money_round(cost),
        money_round(volume),
        money_round(mix),
        money_round(new_eff),
        money_round(disc_eff),
    ]
    rounding = money_round(delta) - sum(effects, dec(0))

    drivers.sort(key=lambda d: abs(d.delta), reverse=True)
    if top_n > 0:
        drivers = drivers[:top_n]

    return ProfitBridge(
        profit_a=money_round(profit_a),
        profit_b=money_round(profit_b),
        delta=money_round(delta),
        price_effect=effects[0],
        cost_effect=effects[1],
        volume_effect=effects[2],
        mix_effect=effects[3],
        new_products_effect=effects[4],
        discontinued_effect=effects[5],
        rounding=rounding,
        common_count=len(common),
        new_count=len(b_only),
        discontinued_count=len(a_only),
        top_drivers=drivers,
    )


def _absent_driver(
    name: str,
    kind: str,
    ln: ProfitProductLine,
    profit_a: Decimal,
    profit_b: Decimal,
    delta: Decimal,
) -> BridgeDriver:
    """A driver for a product present in only one period (no per-effect split)."""
    return BridgeDriver(
        name=name,
        kind=kind,
        qty_a=ln.sell_quantity if kind == "discontinued" else 0.0,
        qty_b=ln.sell_quantity if kind == "new" else 0.0,
        profit_a=money_round(profit_a),
        profit_b=money_round(profit_b),
        delta=money_round(delta),
        price_effect=dec(0),
        cost_effect=dec(0),
        qty_effect=dec(0),
    )
