# chic — MoySklad AI bot

A private Telegram bot that answers business questions about a
[MoySklad](https://www.moysklad.ru/) account, in plain Russian. Messages go to
an LLM agent (DeepSeek for text, OpenAI for photos) that calls **22 read-only
MoySklad tools** exposed over the [Model Context Protocol](https://modelcontextprotocol.io).
Python 3.13, single container.

## How it works

```
Telegram webhook ──► allowlist / dedupe / typing… ──► agent
                                                       │  chat loop (≤6 rounds)
                     DeepSeek (text) / OpenAI (vision) ◄┼─► MCP tools (in-process)
                                                       │        │
                     SQLite app.db (dialog history) ◄──┘        ▼
                                                       22 read-only tools
                                                                │
                                              SQLite cache.db (TTL) ──► MoySklad API
```

- The agent talks to the tools through an in-process MCP server
  (`chic/mcpserver`) — the same tool surface intended for external MCP clients.
- Dialog history lives in `app.db` (durable, on the volume); MoySklad responses
  are cached in `cache.db` (regenerable, TTL per endpoint).
- Guardrails: allowlisted users, per-chat hourly rate limit, per-request token
  stop-loss, round cap, 20 MB photo limit. Every tool is read-only, so there are
  no mutations to confirm.
- Money is returned in the account's base currency (kopecks → rubles converted
  in the aggregation layer).

## Tools

**Data access** — thin wrappers over MoySklad report/entity endpoints:

| Tool | Purpose |
|------|---------|
| `list_products` | Catalog products by name/code/article. |
| `search_assortment` | Unified catalog: products, variants, bundles and services (with `kind`). |
| `get_dashboard` | Quick summary for day/week/month: sales, orders, money, vs previous period. |
| `get_profit` | Profit/revenue/margin over a period, grouped by product/variant/counterparty/saleschannel/employee. |
| `get_sales` | Sales or orders as a time series (quantity + amount per interval), scoped by store/org/project. |
| `get_turnover` | Inventory turnover per product incl. computed turnover-days. |
| `get_stock` | Current stock: on-hand, reserved, available, cost/sale price, value, age (stockDays). |
| `get_stock_by_store` | Stock split by warehouse: units, reserved, available per store. |
| `get_counterparty_metrics` | Per-customer first/last purchase, revenue, avg receipt, returns, balance, profit. |
| `get_money` | Cash flow: in/out/net + time series. |
| `search_documents` | Documents by type (demand, retail sale, order, supply, invoice, cash/commission, return…), date, counterparty, org, store, status. |
| `get_document` | One document with line items and custom attributes. |
| `search_counterparty` | Find customers/suppliers by name/INN/phone/email. |
| `get_audit` | Account change log: who changed what and when, filterable by entity/event. |

**Reference** — resolve the UUIDs the other tools accept:

| Tool | Purpose |
|------|---------|
| `list_references` | List a dictionary — store, organization, saleschannel, employee, project, expenseitem, productfolder, contract, group, country, uom. |
| `list_document_states` | List a document type's status workflow (feed a status id to `search_documents`). |
| `get_account_currency` | The account's base (accounting) currency. |

**Analytics** — computed on top of the data layer:

| Tool | Purpose |
|------|---------|
| `compare_periods` | Compare two periods on revenue/profit; rank top gainers & decliners. |
| `abc_analysis` | Pareto A/B/C of products or customers by revenue/profit. |
| `segment_counterparties` | Rule-based labels: vip, sleeping, at_risk, low_check, debtor, negative_margin. |
| `dead_stock` | Items aged past a threshold with no movement, by tied-up value. |
| `receivables_aging` | Overdue AR from customer invoices, bucketed (current/1-30/31-60/61-90/90+). |

Coverage note: marketplace commissions can be read from commission-report
documents (`search_documents type=commissionreportin/out`); a fully
commission-adjusted margin still needs them booked as expenses or reconciled
against the marketplace's own API. Segmentation is heuristic (RFM rules), not a
predictive model.

## Layout

```
chic/__main__.py   entrypoint: python -m chic (uvicorn)
chic/app.py        FastAPI app: /healthz, Telegram webhook, startup wiring
chic/config.py     env-driven settings
chic/telegram      webhook bot: allowlist, dedupe, typing, photo download
chic/agent         LLM ⇄ MCP chat loop, rate limit, token stop-loss
chic/mcpserver     MCP tool definitions (build_server)
chic/moysklad      MoySklad API client + models + options
chic/aggregate     raw MoySklad → compact DTOs (kopecks → rubles)
chic/cache         cache.db — SQLite TTL response cache (decorator over the client)
chic/store         app.db — durable dialog history (SQLModel + Alembic)
chic/tracing       optional OpenTelemetry / Phoenix instrumentation
```

## Configuration

All settings come from the environment; see [.env.example](.env.example) for the
full, annotated list. Required to run the bot:

- `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`, `ALLOWED_USER_IDS`, `PUBLIC_BASE_URL`
- `MOYSKLAD_TOKEN`
- `DEEPSEEK_API_KEY` (text); `OPENAI_API_KEY` optional — unset disables photo/vision

## Develop

All tooling runs in Docker — nothing on the host but Docker itself.

```sh
make test        # pytest
make lint        # ruff format --check + ruff check
make typecheck   # mypy --strict
make ci          # lint + typecheck + test + security
make migrate     # apply DB migrations to ./app.db
make help        # list every target
```

## Deploy

`make build` produces the production image; it listens on `LISTEN_ADDR`
(default `:8080`) with a health check at `/healthz`, and persists SQLite files at
`CACHE_DB` / `APP_DB` — mount a volume there. Migrations run automatically on
startup. Run it anywhere that runs a container; concrete deployment configs
(servers, domains, secrets) live outside this repo.

## Observability (optional)

Off by default — the project runs fully without it. Set
`PHOENIX_COLLECTOR_ENDPOINT` to an [Arize Phoenix](https://arize.com/phoenix/)
collector (OTLP/HTTP) and the bot emits OpenTelemetry traces with OpenInference
semantics. Each Telegram message is one trace, grouped per user into a Phoenix
**Session**:

- `telegram.message` → `agent.handle` (`AGENT`) → the round loop.
- `LLM` spans carry the full prompt as per-message cards (system, history, user,
  tool results), the tools offered, invocation parameters, the model's tool
  calls, provider routing (DeepSeek vs OpenAI) and token counts.
- `TOOL` spans carry arguments and results; `history.summarize` /
  `memory.consolidate` spans mark the internal LLM passes.
- Outbound HTTP to the LLM providers and MoySklad are child spans.

Every trace is stamped with the build version/commit. Leave the endpoint unset
and the tracer becomes a no-op — nothing exported, zero runtime cost. See the
tracing block in [.env.example](.env.example).
