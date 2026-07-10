# chic — MoySklad AI bot

A Go monolith: a private Telegram bot that answers business questions about a
MoySklad account. Messages go to an LLM agent (DeepSeek for text, OpenAI for
photos) that calls 15 read-only MoySklad [MCP](https://modelcontextprotocol.io)
tools in-process and replies in plain Russian.

## How it works

```
Telegram webhook ──► allowlist / dedupe / typing… ──► agent
                                                       │  chat loop (≤6 rounds)
                     DeepSeek (text) / OpenAI (vision) ◄┼─► MCP client (in-process)
                                                       │        │
                     SQLite app.db (dialog history) ◄──┘        ▼
                                                       mcpserver: 15 tools
                                                                │
                                              SQLite cache.db (TTL) ──► MoySklad API
```

- The MCP server is the same one `-transport stdio` serves to local clients —
  the bot dogfoods the exact public tool surface.
- Dialog history lives in `app.db` (durable, on the volume); MoySklad responses
  are cached in `cache.db` (regenerable, TTL per endpoint).
- Guardrails: per-chat hourly rate limit, per-request token stop-loss, round
  cap, 20 MB photo limit. All tools are read-only, so there are no mutations to
  confirm.

## Tools

15 read-only tools. All monetary values are returned in **rubles** (converted
from MoySklad kopecks in the aggregation layer). Two layers:

**Data access** — thin wrappers over MoySklad report/entity endpoints:

| Tool | Purpose |
|------|---------|
| `list_products` | Catalog products by name/code/article. |
| `get_dashboard` | Quick summary for day/week/month: sales, orders, money, vs previous period. |
| `get_profit` | Profit/revenue/margin over a period, grouped by product/variant/counterparty/saleschannel/employee. |
| `get_turnover` | Inventory turnover per product incl. computed turnover-days. |
| `get_stock` | Current stock: on-hand, reserved, available, cost/sale price, value, age (stockDays). |
| `get_counterparty_metrics` | Per-customer first/last purchase, revenue, avg receipt, returns, balance, profit. |
| `get_money` | Cash flow: in/out/net + time series. |
| `search_documents` | Documents by type (demand, order, supply, invoice, return…), date, counterparty. |
| `get_document` | One document with line items. |
| `search_counterparty` | Find customers/suppliers by name/INN/phone/email. |

**Analytics** — computed on top of the data layer:

| Tool | Purpose |
|------|---------|
| `compare_periods` | Compare two periods on revenue/profit; rank top gainers & decliners (explains *why* it moved). |
| `abc_analysis` | Pareto A/B/C of products or customers by revenue/profit. |
| `segment_counterparties` | Rule-based labels: vip, sleeping, at_risk, low_check, debtor, negative_margin. |
| `dead_stock` | Items aged past a threshold with no movement, by tied-up value. |
| `receivables_aging` | Overdue AR from customer invoices, bucketed (current/1-30/31-60/61-90/90+). |

Coverage note: marketplace unit economics (commissions) isn't a native MoySklad
concept — `get_profit` by sales channel + returns covers revenue/returns, but
commission-adjusted margin needs commissions recorded as expenses or pulled from
the marketplace's own API. Segmentation is heuristic (RFM rules), not a
predictive model.

## Layout

```
cmd/server           entrypoint (env config, graceful shutdown)
internal/telegram    webhook bot: allowlist, dedupe, typing, photo download
internal/agent       LLM⇄MCP chat loop, rate limit, token stop-loss
internal/llm         OpenAI-compatible client (DeepSeek + OpenAI, vision routing)
internal/store       app.db — durable dialog history
internal/moysklad    API client + golden-file tests
internal/aggregate   raw MoySklad -> compact structs (kopecks -> rubles)
internal/mcpserver   MCP tool definitions + transport
internal/cache       cache.db — SQLite TTL response cache (decorator over the client)
```

## Modes

The binary picks a mode via `-transport` (or `MCP_TRANSPORT`), default `bot`:

- `bot` — production: Telegram webhook + `/healthz` + the LLM agent. Requires
  `TELEGRAM_BOT_TOKEN`, `TELEGRAM_WEBHOOK_SECRET`, `ALLOWED_USER_IDS`,
  `PUBLIC_BASE_URL`, `MOYSKLAD_TOKEN` and at least one of `DEEPSEEK_API_KEY` /
  `OPENAI_API_KEY` (photos need the OpenAI one).
- `stdio` — the MoySklad MCP server alone over stdin/stdout, for local MCP
  clients and inspection; only `MOYSKLAD_TOKEN` required.

All knobs are documented in [.env.example](.env.example).

## Run locally (macOS, Apple Silicon)

Pure Go, no CGO — `make build` produces a native `arm64` binary with no runtime
dependencies.

```sh
make build          # -> bin/moysklad-mcp (darwin/arm64)
make test           # full suite with -race
make run-bot        # webhook bot on :8080 (pair with a cloudflared tunnel)
make config         # installs the binary and prints a Claude Desktop config
```

### Claude Desktop / Cursor / other stdio clients

`make config` installs to `~/.local/bin/moysklad-mcp` and prints a snippet to
paste into `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "moysklad": {
      "command": "/Users/you/.local/bin/moysklad-mcp",
      "args": ["-transport", "stdio"],
      "env": {
        "MOYSKLAD_TOKEN": "your-moysklad-token",
        "CACHE_DB": "/Users/you/.moysklad-mcp/cache.db"
      }
    }
  }
}
```

Restart Claude Desktop; the MoySklad tools appear. `CACHE_DB` is optional
(enables the response cache). No auth is needed over stdio — it's a local,
trusted pipe.

## Test

```sh
go test -race ./...
```

Test layers: golden-file client tests (`httptest` + recorded MoySklad JSON),
aggregation unit tests (kopecks/rubles, empty cases), in-process MCP protocol
tests (`tools/list`, `tools/call`, schema validity), LLM payload tests for both
providers, and an end-to-end agent test (scripted LLM → real MCP → fake
MoySklad).

## Deploy

The repo is deployment-agnostic: it ships a [Dockerfile](Dockerfile) that
builds a single static binary image listening on `LISTEN_ADDR` (default
`:8080`) with a health check at `/healthz`, and persists SQLite files at the
paths given by `CACHE_DB`/`APP_DB` — mount a volume there. Run it anywhere
that can run a container; see [.env.example](.env.example) for the required
environment. Concrete deployment configs (servers, domains, secrets) live
outside this repo.
