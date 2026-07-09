# MoySklad MCP Server

Remote [MCP](https://modelcontextprotocol.io) server (Go) that lets LLM clients
(Claude, Antigravity, Cursor…) query MoySklad documents, stock and reports via
tools. Hosted behind Caddy on `https://mcp.chic.md`, transport is **Streamable
HTTP**.

## Status

Foundation in place and tested end-to-end:

- MoySklad HTTP client — static Bearer auth, `45 req / 3s` rate limiter, retry
  with backoff on 429/5xx, offset/limit pagination, `expand`/`filter`/`search`.
- Aggregation layer — compact LLM-friendly structs; **kopecks→rubles conversion
  is centralized here and nowhere else**.
- MCP server over Streamable HTTP with 15 tools (see below) — data access plus
  analytics (ABC, segmentation, dead stock, period comparison, AR aging).
- Auth — static Bearer for simple clients (Antigravity, Cursor) **and** a
  single-user OAuth 2.1 server for Claude: RFC 9728 / RFC 8414 discovery,
  RFC 7591 dynamic client registration, PKCE (S256) authorization-code grant
  behind a password login, and refresh tokens. Both token classes share one
  verifier chain guarding `/mcp`.

- Response cache — an optional SQLite (CGO-free, `modernc.org/sqlite`) TTL cache
  that wraps the client, so repeated/similar analytical queries don't re-hit the
  rate-limited API. Caches exactly what MoySklad returns (no report math is
  recomputed locally); persists across restarts. Enabled via `CACHE_DB`.

Not yet built: proactive background sync/warming (the cache is populated lazily
on first request). Note this is a pull-only MCP server — proactive alerts and
scheduled digests would require a separate scheduler/notifier, out of scope here.

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
internal/moysklad    API client + golden-file tests
internal/aggregate   raw MoySklad -> compact structs (kopecks -> rubles)
internal/mcpserver   MCP tool definitions + transport
internal/auth        Bearer middleware + verifier chain
internal/oauth       single-user OAuth 2.1 server for Claude
internal/cache       SQLite TTL response cache (decorator over the client)
```

## Run locally (macOS, Apple Silicon)

Pure Go, no CGO — `make build` produces a native `arm64` binary with no runtime
dependencies. The easy local path is the **stdio** transport: your client
launches the binary directly, no ports/tokens/TLS.

```sh
make build          # -> bin/moysklad-mcp (darwin/arm64)
make test           # full suite with -race
make config         # installs the binary and prints a Claude Desktop config
```

### Claude Desktop

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

Restart Claude Desktop; the MoySklad tools appear. `CACHE_DB` is optional (enables
the response cache). No auth is needed over stdio — it's a local, trusted pipe.

### Cursor / other stdio clients

Same idea — point the client at the binary with `-transport stdio` and set
`MOYSKLAD_TOKEN` in its env.

### Transports

The binary picks a transport via `-transport` (or `MCP_TRANSPORT`), default
`stdio`:

- `stdio` — local; only `MOYSKLAD_TOKEN` required.
- `http` — remote Streamable HTTP on `:8080/mcp`; also requires
  `MCP_BEARER_TOKEN`, and enables Claude OAuth when `PUBLIC_BASE_URL` +
  `OAUTH_PASSWORD` are set. Used by the Docker deploy below.

## Test

```sh
go test ./...
```

Test layers: golden-file client tests (`httptest` + recorded MoySklad JSON),
aggregation unit tests (kopecks/rubles, empty cases), in-process MCP protocol
tests (`tools/list`, `tools/call`, schema validity), and a real-HTTP transport
smoke test.

## Deploy

Runs on a single Hetzner box shared with other small services (Telegram
backends, webhooks). A shared **edge proxy** (`caddy-docker-proxy`, in a
separate infra repo) terminates TLS for every app and routes to this container
by the `caddy.*` labels in `docker-compose.yml` — no central Caddyfile to edit.

Flow:

1. CI (lint + test + security) runs on every push.
2. On green CI on `main`, the **Deploy** workflow builds the image, pushes it to
   `ghcr.io/mihaildolghintev/chic_mcp`, copies `docker-compose.yml` to the box,
   and runs `docker compose pull && up -d` over SSH.
3. The server holds only `docker-compose.yml` + `.env` (secrets) at `/srv/mcp`
   and the shared external `edge` network — no source, no local build.

First-time server setup (Docker, firewall, `edge` network, DNS, deploy user,
GitHub secrets, the edge proxy) lives in the infra repo's runbook. Required
GitHub Actions secrets: `SSH_HOST`, `SSH_USER`, `SSH_KEY`. Point an `A` record
for `mcp.chic.md` at the box before the first deploy so Let's Encrypt can issue
the cert. Set `MOYSKLAD_TOKEN`, `MCP_BEARER_TOKEN`, `OAUTH_PASSWORD` in the
server's `.env` (see `.env.example`).

## Connect a client

The MCP endpoint is `https://mcp.chic.md/mcp` (Streamable HTTP). There are two
ways to authenticate, depending on the client.

### Claude (Desktop / claude.ai / mobile) — OAuth

Claude drives the OAuth flow itself; you never paste a token.

1. **Settings → Connectors → Add custom connector**.
2. Name it (e.g. `MoySklad`) and set the URL to `https://mcp.chic.md/mcp`.
3. Click **Connect**. A browser page opens asking for a password — enter your
   `OAUTH_PASSWORD`. Claude receives a token and the tools appear.

> Requires `PUBLIC_BASE_URL` and `OAUTH_PASSWORD` to be set on the server.
> Because tokens are held in memory, a server restart signs Claude out and you
> reconnect with the same steps.

### Cursor, Antigravity, and other clients — static Bearer token

These clients send the static `MCP_BEARER_TOKEN` in an `Authorization` header.

**Cursor** — add to `~/.cursor/mcp.json` (or a project's `.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "moysklad": {
      "url": "https://mcp.chic.md/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_MCP_BEARER_TOKEN"
      }
    }
  }
}
```

**Antigravity** and other clients that accept a remote HTTP MCP server use the
same two fields: URL `https://mcp.chic.md/mcp` and header
`Authorization: Bearer YOUR_MCP_BEARER_TOKEN`.

**Claude Code (CLI)** — one command:

```sh
claude mcp add --transport http moysklad https://mcp.chic.md/mcp \
  --header "Authorization: Bearer YOUR_MCP_BEARER_TOKEN"
```

> A client that only supports stdio can bridge to this remote server with
> [`mcp-remote`](https://github.com/geelen/mcp-remote):
> `npx mcp-remote https://mcp.chic.md/mcp --header "Authorization: Bearer YOUR_MCP_BEARER_TOKEN"`.

### Verify the connection

Once connected, ask the client something like *"list products containing coffee"*
— it should call `list_products` and return catalog rows with ruble prices.
