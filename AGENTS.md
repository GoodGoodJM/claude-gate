# AGENTS.md

Guidance for AI coding agents working with this repository.

## What Is This?

Claude Gate is a Claude API reverse proxy that lets multiple Agent/Claude Code instances safely share a pool of personal Claude subscription OAuth tokens. It issues `gate-` prefixed proxy tokens to clients and internally swaps them for real OAuth tokens before forwarding to upstream.

## Tech Stack

- Go 1.26, stdlib-first
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- `jellydator/ttlcache/v3` for in-memory TTL cache
- HTMX + Go `html/template` for Web UI
- GitHub Actions for CI/CD

## Project Structure

```
cmd/claude-gate/       Entry point
internal/admin/        Admin REST API handlers
internal/config/       Env-based configuration
internal/httputil/     HTTP response helpers
internal/logging/      Structured logging (slog) setup
internal/proxy/        Reverse proxy + SSE tapping
internal/store/        SQLite repository layer
  └── migrations/      Embedded SQL migration files
internal/token/        Token pool (round-robin, sticky sessions)
web/                   Embedded web UI (templates + static)
testutil/              Shared test helpers
```

## Commands

```bash
make build          # Build bin/claude-gate (CGO_ENABLED=0)
make test           # go test -race ./...
make lint           # golangci-lint run
make all            # fmt + vet + lint + test + build

# Single package test
go test -race ./internal/store/...
go test -race -run TestPoolRoundRobin ./internal/token/...

# E2E test (requires a real Claude API token)
CLAUDE_GATE_REAL_TOKEN="sk-ant-..." ./scripts/e2e-test.sh

# Local run
CLAUDE_GATE_ADMIN_SECRET="my-secret" ./bin/claude-gate
```

## Architecture

```
Client (Claude Code) → request with gate-token
    ↓
ProxyHandler (proxy.go)
    ├── Validate gate-token (store.GetGateTokenByToken)
    ├── Manager.ResolveToken → sticky session check → miss → round-robin
    ├── httputil.ReverseProxy Director → swap Authorization header with real token
    ├── ModifyResponse → SSE: wrap with tappingReader / JSON: parse directly
    └── usageCh → UsageWriter (async batch) → persist to store
    ↓
Claude API (api.anthropic.com)
```

### Core Flows

1. **Token routing**: `token.Manager` checks sticky session (TTL-based, default 10m) first → miss → `TokenPool.Select(excludeIDs)` for round-robin → bind new sticky session
2. **SSE tapping**: `tappingReader` streams response body through while extracting usage from `message_start` (input_tokens) and `message_delta` (output_tokens) SSE events. Bytes pass through as-is; JSON parsing only triggers on substring match (performance optimization)
3. **Async usage recording**: `UsageWriter` reads entries from a channel and flushes in batches (100 entries or 1s interval). Inserts usage_logs and updates cumulative counters on gate/real tokens

### Store Layer

SQLite WAL mode with separate read/write connections. `writeDB` has `MaxOpenConns=1`, `readDB` has `MaxOpenConns=4`. Migrations live in `internal/store/migrations/` as `{number}_{name}.sql` files and are auto-applied via `//go:embed`.

### Admin API Auth

`Authorization: Bearer <CLAUDE_GATE_ADMIN_SECRET>`. Auth middleware is applied to each route individually (Go 1.22+ method-based mux patterns don't support sub-mux wrapping).

## Code Style

- Format with `gofmt -s`
- `go vet` and `golangci-lint` must pass
- Wrap errors as `fmt.Errorf("context: %w", err)`
- Test files in same package as `_test.go`
- Comments only where needed, godoc style

## Naming Conventions

- Gate token format: `gate-` prefix + base62 32 chars
- Environment variables: `CLAUDE_GATE_` prefix
- Admin API routes: `/admin/api/` prefix
- Proxy: all paths outside `/admin/`

## Pre-Commit Checks

Always run these before committing:

```bash
go fix ./...        # Go 1.26+ code modernization (range-over-int, strings.Builder, etc.)
make lint           # golangci-lint (includes errcheck, etc.)
make test           # go test -race ./...
```

`go fix` auto-converts legacy patterns to modern Go idioms. Run it before lint/test.

Common lint issues:
- **errcheck**: Never ignore returned errors. Use `_ =` for intentional ignores
- **type assertion**: Use comma-ok pattern `v, ok := value.(*Type)` instead of bare `v.(*Type)`

## Development Notes

### SQLite
- `:memory:` databases don't work with the read/write connection split (each connection gets its own DB). Tests must use temp files → use `testutil.NewTestStore()`
- Sticky session `expires_at` must use **UTC**. SQLite `datetime('now')` is UTC-based, so Go code must use `time.Now().UTC()`

### Mux Routing
- Go 1.22+ `http.ServeMux` method-based patterns (`GET /path`) can conflict with method-less patterns (`/path/`). Admin API uses method-based patterns registered individually
- Proxy catch-all `mux.Handle("/", ...)` must be registered **last**

### Token Pool Refresh
- After real token CRUD via admin API, call `Manager.RefreshPool()` (debounced, coalesces within 100ms) to sync the in-memory pool. Without this, newly added tokens won't be available for proxying

### SSE Proxy
- `WriteTimeout: 0` required (prevents SSE streams from hitting timeout)
- `FlushInterval: -1` for immediate flush

### Writing Tests
- FK constraints are enforced: sticky_sessions tests must create actual gate_tokens and real_tokens records first
- `testutil.NewTestStore()`: Creates temp-file-backed SQLite store
- `testutil.NewMockUpstream()`: SSE mock server
- `testutil.NewMockUpstreamJSON()`: JSON mock server

## Deployment

### Docker + Litestream

The Dockerfile includes Litestream for optional SQLite-to-S3 replication. Controlled by the `LITESTREAM_S3_BUCKET` environment variable:
- **Set**: `entrypoint.sh` restores DB from S3 on startup, then runs the app with continuous replication
- **Not set**: App runs directly, no Litestream involved

Key files: `Dockerfile`, `entrypoint.sh`, `litestream.yml`

### CI/CD (GitHub Actions)

- `ci.yml`: lint → test → build. Runs on push/PR to `main`
- `release.yml`: Auto-tags via `mathieudutour/github-tag-action` (Conventional Commits) → Docker build → GHCR push. Runs on push to `main`. Only `feat:` (minor) and `fix:` (patch) trigger a release; other prefixes are skipped (`default_bump: false`)

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CLAUDE_GATE_ADMIN_SECRET` | (required) | Admin API / Web UI auth secret |
| `CLAUDE_GATE_ADDR` | `:8080` | Listen address |
| `CLAUDE_GATE_DB_PATH` | `./claude-gate.db` | SQLite file path |
| `CLAUDE_GATE_UPSTREAM_URL` | `https://api.anthropic.com` | Upstream Claude API URL |
| `CLAUDE_GATE_STICKY_TTL` | `10m` | Sticky session TTL |
| `CLAUDE_GATE_MAX_FAILURES` | `5` | Auto-deactivation failure threshold |
| `CLAUDE_GATE_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `LITESTREAM_S3_BUCKET` | *(optional)* | S3 bucket for Litestream replication. Activates Litestream when set |
| `AWS_REGION` | *(optional)* | AWS region for Litestream S3 replication |

## Dependency Policy

External dependencies: `modernc.org/sqlite` (pure Go SQLite) and `jellydator/ttlcache/v3` (in-memory TTL cache). HTTP, JSON, templates, etc. all use stdlib. Check if stdlib can solve it before adding new dependencies.
