"""Number grounding: check that figures in the answer come from tool results.

The arithmetic is already done in Python (the aggregate layer), so the model can't
*miscompute* a total — but it can still *mis-transcribe* one it was handed
(847320 → 843720) or volunteer a figure no tool returned. Re-fetching cannot catch
that; comparing the answer's numbers against the tool outputs can.

This is a measurement pass, not a gate: :func:`check_answer` returns a report of
which figures could not be traced to a tool result. The agent records it on the
trace span so hallucinated-number regressions are visible in Phoenix; it does not
alter the answer. Only "significant" figures are checked — money-scale amounts,
decimals and percentages — so ordinals ("first 5 rows") and years don't add noise.
"""

from __future__ import annotations

import dataclasses
import json
import re
from collections.abc import Iterable
from decimal import Decimal, InvalidOperation

from chic.aggregate.money import dec

# Thousands may be grouped with a space, NBSP or narrow-NBSP; the fraction uses a
# comma or dot (the answer is Russian-formatted per the system prompt).
_GROUP = "   "
_SEP = "[   ]"
_NUMBER_RE = re.compile(
    r"(?<![\w.,])"
    r"([-−]?)"  # optional minus (hyphen or unicode −)
    r"(\d{1,3}(?:" + _SEP + r"\d{3})+|\d+)"  # grouped (12 345) or plain (1234)
    r"(?:[.,](\d+))?"  # optional fraction
)
# Stripped before scanning: code spans, dates, times, URLs — they carry article
# codes, doc numbers and ids that are not report figures.
_STRIP_RE = re.compile(
    r"```.*?```"  # fenced code
    r"|`[^`]*`"  # inline code
    r"|\bhttps?://\S+"  # urls
    r"|\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}(?::\d{2})?)?"  # iso date/time
    r"|\d{1,2}\.\d{1,2}\.\d{4}",  # dd.mm.yyyy
    re.DOTALL,
)


@dataclasses.dataclass(frozen=True)
class ParsedNumber:
    value: Decimal
    text: str
    is_percent: bool
    grouped: bool
    fractional: bool


@dataclasses.dataclass(frozen=True)
class GroundingReport:
    """Outcome of one grounding pass. ``total`` counts every number found;
    ``checked`` only the significant ones; ``unexplained`` are those with no match."""

    total: int
    checked: int
    grounded: int
    unexplained: list[str]


def extract_numbers(text: str) -> list[ParsedNumber]:
    """Pull candidate figures out of prose, skipping code/date/url spans."""
    cleaned = _STRIP_RE.sub(" ", text)
    out: list[ParsedNumber] = []
    for m in _NUMBER_RE.finditer(cleaned):
        sign, integer, frac = m.group(1), m.group(2), m.group(3)
        grouped = any(ch in _GROUP for ch in integer)
        digits = "".join(ch for ch in integer if ch.isdigit())
        raw = digits + ("." + frac if frac else "")
        try:
            value = dec(raw)
        except InvalidOperation:  # pragma: no cover - regex guarantees digits
            continue
        if sign:
            value = -value
        after = cleaned[m.end() :].lstrip(_GROUP)
        is_percent = after.startswith("%")
        out.append(
            ParsedNumber(
                value=value,
                text=m.group(0).strip(),
                is_percent=is_percent,
                grouped=grouped,
                fractional=frac is not None,
            )
        )
    return out


def _add_json_numbers(obj: object, acc: set[Decimal]) -> None:
    if isinstance(obj, bool):  # bool is an int subclass — not a figure
        return
    if isinstance(obj, int | float):
        try:
            value = dec(obj)
        except InvalidOperation:  # pragma: no cover - defensive
            return
        if value.is_finite():  # drop inf/nan so they can't widen the tolerance
            acc.add(value)
    elif isinstance(obj, dict):
        for v in obj.values():
            _add_json_numbers(v, acc)
    elif isinstance(obj, list):
        for v in obj:
            _add_json_numbers(v, acc)


def collect_known_numbers(outputs: Iterable[str], *, extra: str = "") -> set[Decimal]:
    """Every numeric value a tool returned this turn, plus figures the user typed.

    Tool outputs are JSON; if one was truncated and won't parse, fall back to
    scanning it as text so its figures still count as known.
    """
    known: set[Decimal] = set()
    for raw in outputs:
        try:
            _add_json_numbers(json.loads(raw), known)
        except (json.JSONDecodeError, ValueError):
            for pn in extract_numbers(raw):
                known.add(pn.value)
    for pn in extract_numbers(extra):  # the user may legitimately be restated
        known.add(pn.value)
    return known


def _is_significant(pn: ParsedNumber, min_magnitude: Decimal) -> bool:
    if pn.is_percent or pn.fractional or pn.grouped:
        return True
    # A bare 4-digit integer in a plausible year range is prose, not a figure.
    if pn.value == pn.value.to_integral_value() and 1900 <= pn.value <= 2099:
        return False
    return abs(pn.value) >= min_magnitude


def _matches(n: Decimal, known: set[Decimal], rel_tol: Decimal, abs_tol: Decimal) -> bool:
    for k in known:
        tol = max(abs_tol, abs(k) * rel_tol, abs(n) * rel_tol)
        if abs(n - k) <= tol:
            return True
    return False


def check_answer(
    answer: str,
    known: set[Decimal],
    *,
    min_magnitude: Decimal = Decimal(100),
    rel_tol: Decimal = Decimal("0.001"),
    abs_tol: Decimal = Decimal("1"),
) -> GroundingReport:
    """Report figures in ``answer`` that don't trace to any known tool number.

    ``rel_tol`` absorbs display rounding (e.g. a shown figure a hair off the exact
    Decimal); ``abs_tol`` covers cent-level jitter; ``min_magnitude`` filters out
    small ordinals/counts so the signal targets money-scale transcription.

    ``rel_tol`` is deliberately tight (0.1%): a single-digit slip in a money-scale
    figure — the transcription error this pass exists to catch, e.g. 847320→843720
    (0.4% off) — must land *outside* the window, or the check is blind to its own
    target. Wider slack would let fabricated numbers pass as "close enough".
    """
    numbers = extract_numbers(answer)
    significant = [pn for pn in numbers if _is_significant(pn, min_magnitude)]
    unexplained = [pn.text for pn in significant if not _matches(pn.value, known, rel_tol, abs_tol)]
    return GroundingReport(
        total=len(numbers),
        checked=len(significant),
        grounded=len(significant) - len(unexplained),
        unexplained=unexplained,
    )
