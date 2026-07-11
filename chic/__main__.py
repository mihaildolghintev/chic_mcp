"""Entrypoint: ``python -m chic`` (the container ENTRYPOINT).

Bot mode only for now. The Go build also had a stdio MCP mode selected via
``-transport``/``MCP_TRANSPORT``; that's re-added in a later phase and isn't
used by the deploy.
"""

from __future__ import annotations

import uvicorn

from chic.config import get_settings


def main() -> None:
    settings = get_settings()
    host, port = settings.host_port
    uvicorn.run(
        "chic.app:create_app",
        factory=True,
        host=host,
        port=port,
        log_config=None,  # keep our own logging (setup_logging in create_app)
    )


if __name__ == "__main__":
    main()
