"""MoySklad error envelope.

The API returns ``{"errors":[{...}]}`` on 4xx/5xx; we surface the first error's
message plus the status code.
"""

from __future__ import annotations

import json


class MoyskladError(Exception):
    def __init__(self, status_code: int, message: str = "") -> None:
        self.status_code = status_code
        self.message = message
        super().__init__(f"moysklad: status {status_code}" + (f": {message}" if message else ""))


def parse_api_error(status: int, body: bytes) -> MoyskladError:
    """Best-effort parse; the body may not be JSON."""
    message = ""
    try:
        data = json.loads(body)
        rows = data.get("errors") if isinstance(data, dict) else None
        if rows:
            row = rows[0]
            message = row.get("errorMessage") or row.get("error") or ""
    except (ValueError, AttributeError, IndexError, KeyError):
        pass
    return MoyskladError(status, message)
