# syntax=docker/dockerfile:1
#
# Deploy contract (../chic-deploy/config/deploy.yml — do not change it):
#   - Kamal builds THIS Dockerfile with --build-arg VERSION=$(git describe ...)
#   - cross-built for linux/amd64 from the Apple Silicon laptop
#   - app must listen on :8080 and answer GET /healthz
#   - SQLite lives on the chic_data volume at /data (must be writable by the
#     runtime user — we pre-create /data owned by the app user, like the Go image)
#
# uv-based multi-stage build. All stages share one base so the venv interpreter
# path (/opt/venv) matches across the COPY --from boundary.

FROM ghcr.io/astral-sh/uv:python3.13-bookworm-slim AS base
ENV UV_PROJECT_ENVIRONMENT=/opt/venv \
    UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PATH="/opt/venv/bin:$PATH"
WORKDIR /app

# --- production dependencies only ---
FROM base AS deps
RUN --mount=type=cache,target=/root/.cache/uv \
    --mount=type=bind,source=pyproject.toml,target=pyproject.toml \
    --mount=type=bind,source=uv.lock,target=uv.lock \
    uv sync --frozen --no-install-project --no-dev

# --- dev deps for the tooling container (ruff/mypy/pytest/bandit/pip-audit/uv) ---
# Source is bind-mounted by docker-compose at run time, not copied in.
FROM base AS dev
RUN --mount=type=cache,target=/root/.cache/uv \
    --mount=type=bind,source=pyproject.toml,target=pyproject.toml \
    --mount=type=bind,source=uv.lock,target=uv.lock \
    uv sync --frozen --no-install-project
CMD ["bash"]

# --- runtime image (what chic-deploy ships) ---
FROM base AS runtime
ARG VERSION=dev
ENV APP_VERSION=${VERSION}
COPY --from=deps /opt/venv /opt/venv
# The runtime user owns /data; a fresh chic_data volume inherits this ownership,
# so SQLite can create and write app.db/cache.db.
RUN groupadd --system app \
    && useradd --system --gid app --home-dir /app --no-create-home app \
    && mkdir -p /data && chown app:app /data
COPY --chown=app:app chic/ /app/chic/
USER app
EXPOSE 8080
# Frequent checks + a start grace period so a healthy app is detected within the
# proxy's deploy window even on a cold start (heavy imports + migrations).
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=6 \
    CMD ["python", "-c", "import sys,urllib.request; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8080/healthz', timeout=3).status == 200 else 1)"]
ENTRYPOINT ["python", "-m", "chic"]
