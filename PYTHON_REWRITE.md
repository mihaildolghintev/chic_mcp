# Python rewrite (branch `python-rewrite`)

Big-bang rewrite of the Go app to Python, cut over once at parity. This doc is
the running plan; the Go code stays in-tree as reference until cutover.

## Locked stack

| Concern | Choice |
|---|---|
| Web / webhook | FastAPI + uvicorn |
| Telegram | aiogram 3.x (webhook) |
| Agent | pydantic-ai (rolling summary + memory consolidation stay hand-rolled) |
| LLM | pydantic-ai over OpenAI-compatible APIs — DeepSeek (text) + OpenAI (vision) |
| MCP | official `mcp` SDK / FastMCP (in-process for the agent, streamable HTTP for `/mcp`) |
| Analytics | pandas (forecasting-ready) |
| Storage | SQLAlchemy 2.0 async + Alembic, two-engine single-writer, aiosqlite |
| MoySklad HTTP | httpx + tenacity + aiolimiter |
| Markdown → Telegram | telegramify-markdown (entity output; no custom renderer) |
| Observability | OpenTelemetry + openinference-instrumentation-pydantic-ai + Phoenix |
| Config | pydantic-settings |
| Tooling (Docker-only) | uv · ruff · mypy --strict · pytest · bandit · pip-audit · gitleaks |

## Deploy contract (../chic-deploy — immutable)

Kamal builds this repo's `Dockerfile` (`--build-arg VERSION`), cross-built for
`linux/amd64`. The app must listen on `:8080`, answer `GET /healthz`, read the
env in `chic-deploy/config/deploy.yml`, and use SQLite on `/data`
(`CACHE_DB=/data/cache.db`, `APP_DB=/data/app.db`). Litestream is **not** wired
in the deploy — just the `chic_data` named volume.

## Dev workflow (host needs only Docker)

```sh
make lock       # generate uv.lock
make sync       # build the tooling image
make ci         # lint + typecheck + test + security
make run        # run the runtime image locally (needs .env)
```

## Phases — all complete, `make ci` green (92 tests)

1. ✅ **Scaffold** — uv, Docker + compose + Makefile tooling, pydantic-settings, FastAPI `/healthz`, CI.
2. ✅ **MoySklad client + cache** — async httpx (rate limit, retry w/ `X-Lognex-Retry-After` ms, pagination, id escaping), pydantic models, aiosqlite TTL cache + decorator.
3. ✅ **Analytics** — ABC/RFM/dead-stock/aging/turnover/profit/stock/money/documents, **exact Go rounding parity** (half-away-from-zero, not banker's). Pure Python for parity; pandas is the declared engine for forecasting.
4. ✅ **MCP server** — 16 read-only FastMCP tools, camelCase output, `{items,count}` wrap.
5. ✅ **Agent (pydantic-ai)** — tool loop, `ask_user` (intercepted terminal turn), rolling summary + memory consolidation (hand-rolled: rune budgets + injection-hardening), per-user rate limit, `UsageLimits` stop-loss. MoySklad tools forwarded to the in-process FastMCP via `Tool.from_schema`.
6. ✅ **Telegram (aiogram)** — webhook `feed_update`, allowlist, dedupe, commands, inline keyboards (menu/memory/clarify/feedback), telegramify-markdown (MarkdownV2), photo→data-URI. Fallback = raw text as-is.
7. ✅ **Observability** — OpenTelemetry + `OpenInferenceSpanProcessor` → Phoenix (no-op when endpoint unset) + feedback annotations.
8. ✅ **Deploy** — full `app.py` lifespan (tracing→store→cache→MoySklad→MCP→agent→bot), production image builds & serves `:8080 /healthz`.

### Known follow-ups (not blocking the bot)
- **Public `/mcp` endpoint**: the agent uses FastMCP in-process; the external `/mcp` HTTP endpoint (Go's `MCP_BEARER_TOKEN`-gated route) is not wired yet — the deploy leaves it off, so prod is unaffected.
- **Model ids**: `deepseek-v4-flash` / `gpt-5.4-mini` carried over from Go as defaults — verify against provider docs before first real traffic (config.py TODO).
- **Cutover**: ✅ done — the Go tree (`cmd/`, `internal/`, `go.mod`, `go.sum`, `.golangci.yml`, `scripts/ci.sh`) has been removed; the project is Python-only.
