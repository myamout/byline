# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Byline is a content aggregation platform that polls Reddit RSS feeds and the OSSInsight trending repos API, normalizes the data, and persists it to PostgreSQL. The backend is written in Go; the `app/` directory is a placeholder for future frontend work.

## Development Setup

- **Go version:** 1.25.5 (managed via [mise](https://mise.jdx.dev/))
- **Environment:** mise loads `.env.local` automatically (put `DATABASE_URL` there for local dev)
- **Database:** PostgreSQL (any provider — Neon, local Docker, etc.) via `pgx/v5` + `pgxpool`

## Commands

```bash
mise run backend:test            # Run all tests (store integration tests skipped without DB)
mise run backend:poller          # Run the poller daemon
mise run db:migrate:up           # Apply all pending migrations
mise run db:migrate:down         # Rollback last migration
mise run db:migrate:version      # Show current migration version
VERSION=N mise run db:migrate:force  # Force migration to version N
```

Run a single test:
```bash
cd backend && go test -v -run TestFunctionName ./internal/reddit/
```

Run store integration tests (requires running Postgres):
```bash
BYLINE_TEST_DATABASE_URL="postgres://user:pass@localhost:5432/dbname?sslmode=disable" \
  go test -v ./internal/store/...
```

## Architecture

All backend code lives under `backend/`. There are four internal packages and two CLI entry points:

```
backend/
  cmd/
    poller/main.go         # Polling daemon entry point
    migrate/main.go        # Database migration CLI (up/down/version/force)
  internal/
    reddit/                # RSS feed parser → Article structs
    ossinsight/            # HTTP client for OSSInsight trending API → Repository structs
    googlenews/            # Google News RSS parser → Article structs
    poller/                # Orchestrator: Source interface, adapters, FeedItem normalization
    store/                 # Persistence: Store interface, PostgresStore, migrations
```

### Data flow

```
Poller.Run(ctx) spawns one goroutine per source
  │
  ├─ RedditSource.Fetch(ctx)
  │    reddit.Parser.ParseSubreddit → []Article → toFeedItem() → []FeedItem
  │
  ├─ OSSInsightSource.Fetch(ctx)
  │    ossinsight.Client.GetTrendingRepos → []Repository → repoToFeedItem() → []FeedItem
  │
  └─ GoogleNewsSource.Fetch(ctx)
       googlenews.Parser.ParseFeed → []Article → newsToFeedItem() → []FeedItem
  │
  v
Poller.persist() routes by source name:
  "reddit"     → feedItemToArticle()  → store.UpsertArticles()    (ON CONFLICT upsert)
  "ossinsight" → feedItemToRepository() → store.InsertTrendingRepos() (point-in-time snapshot)
  "googlenews" → feedItemToNewsArticle() → store.UpsertNewsArticles()  (ON CONFLICT upsert)
```

### Key packages

**`reddit`** — `Parser` wraps `gofeed.Parser` + compiled regex. Entry points: `ParseSubreddit`, `ParseURL`, `ParseString`. Filters self-posts via domain checks. Output: `Article` struct.

**`ossinsight`** — `Client` wraps `http.Client` for `GET /v1/trending/repos`. Configurable via `TrendingReposOptions` (Period, Language). Output: `Repository` struct.

**`googlenews`** — `Parser` wraps `gofeed.Parser` + `http.Client`. Entry points: `ParseFeed` (fetches + resolves redirects), `ParseString` (for testing). Resolves Google News redirect URLs to actual article URLs via HTTP HEAD. Output: `Article` struct.

**`poller`** — The orchestrator. `Source` interface (`Name()` + `Fetch(ctx)`) with `RedditSource` and `OSSInsightSource` adapters. Each source runs in its own goroutine with an independent `time.Ticker`. `FeedItem` is the normalized intermediate type. The `persist` method converts back to domain types and calls the appropriate `store.Store` method. `LogStore` implements `store.Store` for development without a database.

**`store`** — `Store` interface with typed CRUD methods for both data sources. `PostgresStore` uses `pgxpool` with batch transactions, cursor-based pagination (`ListOptions`), and `ON CONFLICT` upserts for Reddit deduplication. Sentinel errors: `ErrNotFound`, `ErrDuplicateKey`. Migrations in `internal/store/migrations/` managed by `golang-migrate`.

### Concurrency model

- One goroutine per source with independent ticker intervals (default: 5m Reddit, 1h OSSInsight, 30m Google News)
- Per-fetch `context.WithTimeout` (default 30s) prevents hung requests
- `sync.WaitGroup` for clean drain on shutdown
- `signal.NotifyContext` handles SIGINT/SIGTERM in the poller entry point
- `pgxpool.Pool` is safe for concurrent use across source goroutines

## Environment Variables

| Variable | Used by | Purpose |
|----------|---------|---------|
| `DATABASE_URL` | `cmd/poller`, `cmd/migrate` | PostgreSQL connection string. Poller falls back to `LogStore` if unset. |
| `BYLINE_TEST_DATABASE_URL` | `store/postgres_test.go` | Test database URL. Integration tests are skipped if unset. |

## Testing

- **Unit tests** use `httptest.Server` for HTTP mocking and `reddit.Parser.ParseString` for feed parsing
- **Poller tests** use `fakeSource` (controllable items/errors) and `recordingStore` (tracks which store methods were called)
- **Store integration tests** are gated behind `BYLINE_TEST_DATABASE_URL` — they skip cleanly without a database, so `go test ./...` always passes
- All tests run with `-race` in development

## CI

GitHub Actions (`.github/workflows/ci.yaml`) runs `mise run backend:test` on pushes to main and pull requests targeting main.
