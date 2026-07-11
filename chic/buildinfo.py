"""Build metadata.

The container image bakes the app repo's ``git describe`` into ``APP_VERSION``
at build time (Dockerfile ``ARG VERSION`` → ``ENV APP_VERSION``), mirroring the
Go build's ``-ldflags -X ...buildinfo.Version``. chic-deploy passes it via
``--build-arg VERSION=$(git describe --tags --always --dirty)``.
"""

from __future__ import annotations

import os
import platform

APP_VERSION: str = os.environ.get("APP_VERSION", "dev")


def build_info() -> dict[str, str]:
    return {
        "version": APP_VERSION,
        "python": platform.python_version(),
    }
