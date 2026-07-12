"""Phoenix feedback annotations — the 👍/👎 write path.

Writes span annotations through the official ``arize-phoenix-client`` so an audit
can filter traces down to thumbs-down dialogs. Best-effort and a no-op when Phoenix
isn't configured. Auth (``PHOENIX_API_KEY`` / ``PHOENIX_CLIENT_HEADERS``) is read
from the environment by the client itself.
"""

from __future__ import annotations

import logging
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from phoenix.client import AsyncClient

logger = logging.getLogger(__name__)


class PhoenixAnnotator:
    def __init__(self, endpoint: str | None) -> None:
        endpoint = (endpoint or "").strip()
        self._client: AsyncClient | None = None
        if endpoint:
            from phoenix.client import AsyncClient

            self._client = AsyncClient(base_url=endpoint)

    @property
    def enabled(self) -> bool:
        return self._client is not None

    async def annotate(
        self,
        span_id: str,
        *,
        name: str,
        label: str,
        score: float,
        identifier: str = "",
    ) -> None:
        if self._client is None or not span_id:
            return
        try:
            await self._client.spans.add_span_annotation(
                span_id=span_id,
                annotation_name=name,
                annotator_kind="HUMAN",
                label=label,
                score=score,
                identifier=identifier,
                sync=False,
            )
        except Exception:
            logger.warning("phoenix annotation failed", exc_info=True)
