"""Phoenix feedback annotations — the 👍/👎 write path.

POSTs a span annotation to Phoenix's REST API so an audit can filter traces down
to thumbs-down dialogs. Best-effort and no-op when Phoenix isn't configured.
"""

from __future__ import annotations

import logging
from urllib.parse import unquote

import httpx

logger = logging.getLogger(__name__)


def _parse_headers(raw: str) -> dict[str, str]:
    """Parse ``k=v,k2=v2`` (OTEL_EXPORTER_OTLP_HEADERS), percent-decoding values."""
    out: dict[str, str] = {}
    for pair in raw.split(","):
        pair = pair.strip()
        if not pair or "=" not in pair:
            continue
        key, _, value = pair.partition("=")
        out[key.strip()] = unquote(value.strip())
    return out


class PhoenixAnnotator:
    def __init__(self, endpoint: str, headers: dict[str, str] | None = None) -> None:
        endpoint = (endpoint or "").strip()
        self._enabled = bool(endpoint)
        self._url = endpoint.rstrip("/") + "/v1/span_annotations?sync=false" if endpoint else ""
        self._headers = {"Content-Type": "application/json", **(headers or {})}

    @classmethod
    def from_env(cls, endpoint: str | None, headers_env: str = "") -> PhoenixAnnotator:
        return cls(endpoint or "", _parse_headers(headers_env))

    @property
    def enabled(self) -> bool:
        return self._enabled

    async def annotate(
        self,
        span_id: str,
        *,
        name: str,
        label: str,
        score: float,
        identifier: str = "",
    ) -> None:
        if not self._enabled or not span_id:
            return
        payload = {
            "data": [
                {
                    "span_id": span_id,
                    "name": name,
                    "annotator_kind": "HUMAN",
                    "result": {"label": label, "score": score},
                    "identifier": identifier,
                }
            ]
        }
        try:
            async with httpx.AsyncClient(timeout=5.0) as client:
                resp = await client.post(self._url, json=payload, headers=self._headers)
            if resp.status_code >= 300:
                logger.warning("phoenix annotation rejected: %d", resp.status_code)
        except httpx.HTTPError:
            logger.warning("phoenix annotation failed", exc_info=True)
