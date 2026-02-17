# Postgres CRUD Client Implementation Plan

## 1. Codebase Summary

The project has two data source clients:

- **`backend/internal/reddit/`** — Parses Reddit RSS feeds and produces `Article` structs with six fields: `Author`, `Subreddit`, `PostedAt` (time.Time), `ArticleLink`, `Title`, `RedditLink`.

- **`backend/internal/ossinsight/`** — HTTP client for the OSSInsight API producing `Repository` structs with eleven fields: `RepoID` (int), `RepoName`, `PrimaryLanguage`, `Description`, `Stars` (int), `Forks` (int), `PullRequests` (int), `Pushes` (int), `TotalScore` (int), `ContributorLogins`, `CollectionNames`.

The module is `github.com/myamout/byline`, Go 1.25.5, with code under `backend/internal/`. Tests use the standard library `testing` package with table-driven tests. There are no ORMs, no existing database dependencies, and no configuration management libraries.

---

## 2. Recommended Library: pgx v5

**Choice: `github.com/jackc/pgx/v5`**

### Rationale

- **pgx** is the de facto standard Go Postgres driver. It is a pure-Go implementation with no CGO dependency, making builds and cross-compilation straightforward.
- It implements `database/sql` compatibility via `pgx/v5/stdlib`, but also exposes a native interface with better performance (binary protocol, batch queries, COPY support, type-safe parameter binding).
- It includes a built-in connection pool (`pgx/v5/pgxpool`) that handles health checks, idle connection reaping, and context-aware acquire/release — eliminating the need for a separate pooling library.
- The connection string format (`postgres://user:password@host:5432/dbname?sslmode=require`) works identically across Neon, AWS RDS, Supabase, local Docker, and any other Postgres provider. No provider-specific code is needed.
- pgx uses `pgtype` for native Postgres type mapping (timestamps with timezone, arrays, JSON columns), which gives better fidelity than `database/sql` scanning.
- It is the most actively maintained Go Postgres library with the largest community.

### Migrations

For migrations, we'll use **`github.com/golang-migrate/migrate/v4`** with the `postgres` driver. It is lightweight, supports both up and down migrations from plain SQL files, and has no opinion on your query layer.

---

## 3. Package Structure

```
backend/
  internal/
    reddit/
      parser.go           # (existing)
      parser_test.go      # (existing)
    ossinsight/
      client.go           # (existing)
      client_test.go      # (existing)
    store/
      store.go            # Store interface + common types (errors, options)
      postgres.go         # pgxpool-backed implementation of Store
      postgres_test.go    # Integration tests against a real/test Postgres
      migrations/
        000001_create_reddit_articles.up.sql
        000001_create_reddit_articles.down.sql
        000002_create_trending_repos.up.sql
        000002_create_trending_repos.down.sql
```

### Why a Single `store` Package

The project has exactly two entity types today. Splitting into sub-packages would create unnecessary indirection and import cycles when both tables need to participate in the same transaction or share the same connection pool. A single `store` package with a unified interface keeps things simple. If the project grows to dozens of entity types, the package can be refactored into domain-specific sub-packages at that time.

---

## 4. Database Schema

### 4.1 Reddit Articles Table

```sql
-- 000001_create_reddit_articles.up.sql

CREATE TABLE IF NOT EXISTS reddit_articles (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    author          TEXT        NOT NULL,
    subreddit       TEXT        NOT NULL,
    posted_at       TIMESTAMPTZ NOT NULL,
    article_link    TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    reddit_link     TEXT        NOT NULL,

    -- Deduplication: the same article link should not appear twice for the same subreddit.
    -- This also serves as the conflict target for upserts.
    CONSTRAINT uq_reddit_articles_link UNIQUE (subreddit, article_link),

    -- Housekeeping timestamps.
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Query pattern: "get latest articles for a subreddit, ordered by posted_at desc"
CREATE INDEX idx_reddit_articles_subreddit_posted
    ON reddit_articles (subreddit, posted_at DESC);

-- Query pattern: "find article by its external link"
CREATE INDEX idx_reddit_articles_article_link
    ON reddit_articles (article_link);
```

```sql
-- 000001_create_reddit_articles.down.sql

DROP TABLE IF EXISTS reddit_articles;
```

#### Design Decisions

- `BIGINT GENERATED ALWAYS AS IDENTITY` over UUID: sequential IDs are more cache-friendly for B-tree indexes and simpler for pagination. There is no distributed-write scenario requiring UUIDs.
- `TIMESTAMPTZ` for all timestamps: avoids timezone ambiguity. `posted_at` comes from the feed; `created_at` / `updated_at` track database-level lifecycle.
- The `UNIQUE(subreddit, article_link)` constraint enables `INSERT ... ON CONFLICT` upsert semantics: when the same article is re-scraped, the row is updated rather than duplicated.
- The composite index on `(subreddit, posted_at DESC)` directly supports the primary read pattern of fetching recent articles per subreddit.

### 4.2 Trending Repositories Table

```sql
-- 000002_create_trending_repos.up.sql

CREATE TABLE IF NOT EXISTS trending_repos (
    id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    repo_id             INT         NOT NULL,
    repo_name           TEXT        NOT NULL,
    primary_language    TEXT        NOT NULL DEFAULT '',
    description         TEXT        NOT NULL DEFAULT '',
    stars               INT         NOT NULL DEFAULT 0,
    forks               INT         NOT NULL DEFAULT 0,
    pull_requests       INT         NOT NULL DEFAULT 0,
    pushes              INT         NOT NULL DEFAULT 0,
    total_score         INT         NOT NULL DEFAULT 0,
    contributor_logins  TEXT        NOT NULL DEFAULT '',
    collection_names    TEXT        NOT NULL DEFAULT '',

    -- Tracks which period+language query produced this row.
    trending_period     TEXT        NOT NULL,
    trending_language   TEXT        NOT NULL DEFAULT '',

    -- When this trending snapshot was captured.
    fetched_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Deduplication: same repo should not appear twice for the same
    -- period+language+fetch-date combination.
    CONSTRAINT uq_trending_repos_snapshot UNIQUE (repo_id, trending_period, trending_language, fetched_at),

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Query pattern: "get trending repos by language and period, most recent first"
CREATE INDEX idx_trending_repos_language_period_fetched
    ON trending_repos (trending_language, trending_period, fetched_at DESC);

-- Query pattern: "find all trending snapshots for a specific repo"
CREATE INDEX idx_trending_repos_repo_id
    ON trending_repos (repo_id);

-- Query pattern: "get repos by score"
CREATE INDEX idx_trending_repos_total_score
    ON trending_repos (total_score DESC);
```

```sql
-- 000002_create_trending_repos.down.sql

DROP TABLE IF EXISTS trending_repos;
```

#### Design Decisions

- The trending data is inherently temporal (a repo trends for a period and then may not). Rather than overwriting rows, each fetch creates a historical snapshot. This enables trend analysis over time ("was this repo trending last week too?").
- `fetched_at` records when the API was called, distinct from any upstream timestamp. This is critical for historical queries.
- `trending_period` and `trending_language` record the query parameters that produced the result, since the same repo can appear in multiple period/language combinations.
- The unique constraint on `(repo_id, trending_period, trending_language, fetched_at)` prevents duplicate inserts from the same scrape run while still allowing the same repo to appear in different snapshots.

---

## 5. Interface Design

```go
// backend/internal/store/store.go
package store

import (
    "context"
    "time"

    "github.com/myamout/byline/backend/internal/ossinsight"
    "github.com/myamout/byline/backend/internal/reddit"
)

// Store defines the persistence operations for Byline's data sources.
// Implementations must be safe for concurrent use.
type Store interface {
    // --- Reddit Articles ---

    // UpsertArticle inserts a Reddit article or updates it if the
    // (subreddit, article_link) pair already exists.
    UpsertArticle(ctx context.Context, article reddit.Article) (int64, error)

    // UpsertArticles batch-upserts multiple articles in a single transaction.
    // Returns the number of rows affected.
    UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error)

    // GetArticleByID retrieves a single article by its database ID.
    GetArticleByID(ctx context.Context, id int64) (*reddit.Article, error)

    // ListArticles retrieves articles for a subreddit, ordered by posted_at desc.
    // Supports cursor-based pagination via the opts parameter.
    ListArticles(ctx context.Context, subreddit string, opts ListOptions) ([]reddit.Article, error)

    // DeleteArticle removes an article by its database ID.
    // Returns true if a row was deleted, false if no row matched.
    DeleteArticle(ctx context.Context, id int64) (bool, error)

    // --- Trending Repositories ---

    // InsertTrendingRepo stores a single trending repository snapshot.
    InsertTrendingRepo(ctx context.Context, repo ossinsight.Repository, period string, language string) (int64, error)

    // InsertTrendingRepos batch-inserts multiple trending repositories in a single transaction.
    // Returns the number of rows inserted.
    InsertTrendingRepos(ctx context.Context, repos []ossinsight.Repository, period string, language string) (int64, error)

    // GetTrendingRepoByID retrieves a single trending repo record by its database ID.
    GetTrendingRepoByID(ctx context.Context, id int64) (*TrendingRepoRecord, error)

    // ListTrendingRepos retrieves trending repos, optionally filtered by
    // language, period, and time range.
    ListTrendingRepos(ctx context.Context, filter TrendingRepoFilter, opts ListOptions) ([]TrendingRepoRecord, error)

    // DeleteTrendingRepo removes a trending repo record by its database ID.
    // Returns true if a row was deleted, false if no row matched.
    DeleteTrendingRepo(ctx context.Context, id int64) (bool, error)

    // --- Lifecycle ---

    // Ping verifies the database connection is alive.
    Ping(ctx context.Context) error

    // Close releases all database resources.
    Close()
}

// ListOptions controls pagination for list queries.
type ListOptions struct {
    // Limit is the maximum number of rows to return. Default: 50, Max: 200.
    Limit int

    // Cursor is an opaque pagination token (the ID of the last item seen).
    // Pass 0 for the first page.
    Cursor int64
}

// TrendingRepoFilter controls filtering for trending repo queries.
type TrendingRepoFilter struct {
    // Language filters by programming language. Empty means all languages.
    Language string

    // Period filters by trending period. Empty means all periods.
    Period string

    // FetchedAfter limits results to snapshots fetched after this time.
    FetchedAfter time.Time

    // FetchedBefore limits results to snapshots fetched before this time.
    FetchedBefore time.Time
}

// TrendingRepoRecord extends ossinsight.Repository with database metadata.
type TrendingRepoRecord struct {
    ID               int64
    ossinsight.Repository
    TrendingPeriod   string
    TrendingLanguage string
    FetchedAt        time.Time
    CreatedAt        time.Time
    UpdatedAt        time.Time
}
```

### Interface Design Rationale

- **Accepts domain types directly** (`reddit.Article`, `ossinsight.Repository`): The store layer translates between domain structs and database rows. Callers do not need to know about database-specific types, IDs, or timestamps.
- **Upsert over Insert for Reddit**: Articles are scraped repeatedly from RSS feeds. Upsert with the unique constraint on `(subreddit, article_link)` is idempotent, which simplifies the scraping logic (no need to pre-check for duplicates).
- **Insert (not Upsert) for Trending Repos**: Each scrape captures a new point-in-time snapshot. Duplicate inserts for the same `fetched_at` are prevented by the unique constraint, but conceptually each fetch is additive.
- **Batch methods**: Both `UpsertArticles` and `InsertTrendingRepos` wrap their operations in a single transaction using pgx batch/pipeline. This is critical for performance when processing an entire feed (25+ items) or trending list (30+ repos).
- **Cursor-based pagination over offset**: Cursor pagination (keyset pagination) is O(1) regardless of page depth, while offset pagination degrades as offset grows. The cursor is simply the `id` column of the last row returned.
- **`Ping` and `Close`**: Standard lifecycle methods. `Ping` is used by health check endpoints. `Close` releases the connection pool on shutdown.

---

## 6. Connection Management

```go
// backend/internal/store/postgres.go (connection setup excerpt)

package store

import (
    "context"
    "fmt"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store using pgxpool.
type PostgresStore struct {
    pool *pgxpool.Pool
}

// NewPostgresStore creates a new PostgresStore from a standard Postgres connection
// string. The connection string format works with any Postgres provider:
//
//   postgres://user:password@host:5432/dbname?sslmode=require
//
func NewPostgresStore(ctx context.Context, connString string) (*PostgresStore, error) {
    config, err := pgxpool.ParseConfig(connString)
    if err != nil {
        return nil, fmt.Errorf("parsing connection string: %w", err)
    }

    // Pool tuning -- sensible defaults for a scraper workload.
    config.MaxConns = 10
    config.MinConns = 2
    config.MaxConnLifetime = 30 * time.Minute
    config.MaxConnIdleTime = 5 * time.Minute
    config.HealthCheckPeriod = 30 * time.Second

    pool, err := pgxpool.NewWithConfig(ctx, config)
    if err != nil {
        return nil, fmt.Errorf("creating connection pool: %w", err)
    }

    // Verify connectivity immediately.
    if err := pool.Ping(ctx); err != nil {
        pool.Close()
        return nil, fmt.Errorf("pinging database: %w", err)
    }

    return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Ping(ctx context.Context) error {
    return s.pool.Ping(ctx)
}

func (s *PostgresStore) Close() {
    s.pool.Close()
}
```

### Provider-Agnostic Design

- The constructor takes a single `connString` parameter. Neon, Supabase, RDS, local Docker — they all use the same `postgres://` URI scheme with different host/credentials.
- SSL mode is part of the connection string (`?sslmode=require` for Neon/cloud, `?sslmode=disable` for local dev). No provider-specific code.
- The connection string should come from an environment variable (e.g., `DATABASE_URL`), loaded at the application entry point and passed down.

---

## 7. CRUD Method Implementations

### 7.1 UpsertArticle

```go
func (s *PostgresStore) UpsertArticle(ctx context.Context, article reddit.Article) (int64, error) {
    var id int64
    err := s.pool.QueryRow(ctx, `
        INSERT INTO reddit_articles (author, subreddit, posted_at, article_link, title, reddit_link)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (subreddit, article_link) DO UPDATE SET
            author     = EXCLUDED.author,
            title      = EXCLUDED.title,
            reddit_link = EXCLUDED.reddit_link,
            posted_at  = EXCLUDED.posted_at,
            updated_at = NOW()
        RETURNING id
    `, article.Author, article.Subreddit, article.PostedAt,
       article.ArticleLink, article.Title, article.RedditLink,
    ).Scan(&id)
    if err != nil {
        return 0, fmt.Errorf("upserting article: %w", err)
    }
    return id, nil
}
```

### 7.2 UpsertArticles (Batch)

```go
func (s *PostgresStore) UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error) {
    if len(articles) == 0 {
        return 0, nil
    }

    tx, err := s.pool.Begin(ctx)
    if err != nil {
        return 0, fmt.Errorf("beginning transaction: %w", err)
    }
    defer tx.Rollback(ctx)

    batch := &pgx.Batch{}
    for _, a := range articles {
        batch.Queue(`
            INSERT INTO reddit_articles (author, subreddit, posted_at, article_link, title, reddit_link)
            VALUES ($1, $2, $3, $4, $5, $6)
            ON CONFLICT (subreddit, article_link) DO UPDATE SET
                author     = EXCLUDED.author,
                title      = EXCLUDED.title,
                reddit_link = EXCLUDED.reddit_link,
                posted_at  = EXCLUDED.posted_at,
                updated_at = NOW()
        `, a.Author, a.Subreddit, a.PostedAt, a.ArticleLink, a.Title, a.RedditLink)
    }

    br := tx.SendBatch(ctx, batch)
    var affected int64
    for range articles {
        ct, err := br.Exec()
        if err != nil {
            br.Close()
            return 0, fmt.Errorf("executing batch upsert: %w", err)
        }
        affected += ct.RowsAffected()
    }
    br.Close()

    if err := tx.Commit(ctx); err != nil {
        return 0, fmt.Errorf("committing transaction: %w", err)
    }
    return affected, nil
}
```

### 7.3 ListArticles (Cursor Pagination)

```go
func (s *PostgresStore) ListArticles(ctx context.Context, subreddit string, opts ListOptions) ([]reddit.Article, error) {
    limit := clampLimit(opts.Limit)

    rows, err := s.pool.Query(ctx, `
        SELECT id, author, subreddit, posted_at, article_link, title, reddit_link
        FROM reddit_articles
        WHERE subreddit = $1
          AND ($2::bigint = 0 OR id < $2)
        ORDER BY posted_at DESC, id DESC
        LIMIT $3
    `, subreddit, opts.Cursor, limit)
    if err != nil {
        return nil, fmt.Errorf("listing articles: %w", err)
    }
    defer rows.Close()

    var articles []reddit.Article
    for rows.Next() {
        var (
            id int64
            a  reddit.Article
        )
        if err := rows.Scan(&id, &a.Author, &a.Subreddit, &a.PostedAt,
            &a.ArticleLink, &a.Title, &a.RedditLink); err != nil {
            return nil, fmt.Errorf("scanning article row: %w", err)
        }
        articles = append(articles, a)
    }
    return articles, rows.Err()
}
```

### 7.4 GetArticleByID / DeleteArticle

```go
func (s *PostgresStore) GetArticleByID(ctx context.Context, id int64) (*reddit.Article, error) {
    var a reddit.Article
    err := s.pool.QueryRow(ctx, `
        SELECT author, subreddit, posted_at, article_link, title, reddit_link
        FROM reddit_articles
        WHERE id = $1
    `, id).Scan(&a.Author, &a.Subreddit, &a.PostedAt,
        &a.ArticleLink, &a.Title, &a.RedditLink)
    if err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            return nil, ErrNotFound
        }
        return nil, fmt.Errorf("getting article by ID: %w", err)
    }
    return &a, nil
}

func (s *PostgresStore) DeleteArticle(ctx context.Context, id int64) (bool, error) {
    ct, err := s.pool.Exec(ctx, `DELETE FROM reddit_articles WHERE id = $1`, id)
    if err != nil {
        return false, fmt.Errorf("deleting article: %w", err)
    }
    return ct.RowsAffected() > 0, nil
}
```

The trending repo methods follow the same patterns, with `InsertTrendingRepo` / `InsertTrendingRepos` using plain `INSERT` (no upsert), and `ListTrendingRepos` applying the `TrendingRepoFilter` fields as optional WHERE clauses.

---

## 8. Error Handling Pattern

```go
// backend/internal/store/store.go (error declarations)

import "errors"

var (
    // ErrNotFound is returned when a requested record does not exist.
    ErrNotFound = errors.New("store: record not found")

    // ErrDuplicateKey is returned when an insert violates a unique constraint
    // and no ON CONFLICT clause was used.
    ErrDuplicateKey = errors.New("store: duplicate key")
)
```

Internal implementation translates pgx-specific errors to these sentinel errors:

```go
import "github.com/jackc/pgx/v5/pgconn"

func classifyError(err error) error {
    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) {
        switch pgErr.Code {
        case "23505": // unique_violation
            return fmt.Errorf("%w: %s", ErrDuplicateKey, pgErr.Detail)
        }
    }
    return err
}
```

All public methods wrap errors with `fmt.Errorf("operation description: %w", err)` to maintain a clear error chain. Callers can use `errors.Is(err, store.ErrNotFound)` for control flow without coupling to pgx internals.

---

## 9. Migrations Strategy

**Tool: `golang-migrate/migrate`**

Migration files live in `backend/internal/store/migrations/` as plain SQL. Each migration has an up and down file, numbered sequentially.

### Running Migrations Programmatically

Called at application startup or via a CLI subcommand:

```go
import (
    "github.com/golang-migrate/migrate/v4"
    _ "github.com/golang-migrate/migrate/v4/database/postgres"
    _ "github.com/golang-migrate/migrate/v4/source/file"
)

func RunMigrations(connString string, migrationsPath string) error {
    m, err := migrate.New(
        "file://"+migrationsPath,
        connString,
    )
    if err != nil {
        return fmt.Errorf("creating migrator: %w", err)
    }
    defer m.Close()

    if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
        return fmt.Errorf("running migrations: %w", err)
    }
    return nil
}
```

### Embedded Migrations (Recommended for Production)

Embed the migration files using `embed.FS` and use the `iofs` source driver so the migration SQL ships inside the binary:

```go
//go:embed migrations/*.sql
var migrationsFS embed.FS
```

This is the recommended approach for production so that migration files do not need to be shipped separately alongside the binary.

---

## 10. Testing Approach

The project currently uses only the standard library `testing` package with table-driven tests and `httptest` for HTTP mocking. The database tests follow the same style.

### 10.1 Integration Tests Against Real Postgres

Database store tests run against a real Postgres instance. The recommended approach:

```go
// backend/internal/store/postgres_test.go

package store

import (
    "context"
    "os"
    "testing"
)

// testDSN reads the test database connection string from the environment.
// Tests are skipped if the variable is not set, allowing `go test ./...`
// to pass in environments without Postgres (e.g., CI without a service container).
func testDSN(t *testing.T) string {
    t.Helper()
    dsn := os.Getenv("BYLINE_TEST_DATABASE_URL")
    if dsn == "" {
        t.Skip("BYLINE_TEST_DATABASE_URL not set; skipping integration test")
    }
    return dsn
}

func setupTestStore(t *testing.T) *PostgresStore {
    t.Helper()
    ctx := context.Background()
    dsn := testDSN(t)

    s, err := NewPostgresStore(ctx, dsn)
    if err != nil {
        t.Fatalf("creating test store: %v", err)
    }

    // Run migrations to ensure schema is current.
    // Clean tables before each test for isolation.
    t.Cleanup(func() {
        s.pool.Exec(ctx, "TRUNCATE reddit_articles, trending_repos RESTART IDENTITY")
        s.Close()
    })

    return s
}
```

### 10.2 Test Cases

For each CRUD method, the following test scenarios:

#### Reddit Articles

- `TestUpsertArticle_Insert` — inserts a new article, verifies returned ID and retrievability.
- `TestUpsertArticle_Update` — upserts an existing article with changed title, verifies the row is updated (not duplicated).
- `TestUpsertArticles_Batch` — batch upserts 10 articles, verifies count.
- `TestUpsertArticles_EmptySlice` — passes empty slice, verifies no error and 0 rows.
- `TestGetArticleByID_NotFound` — queries a non-existent ID, verifies `ErrNotFound`.
- `TestListArticles_Pagination` — inserts 15 articles, pages through with limit=5, verifies ordering and cursor correctness.
- `TestListArticles_EmptySubreddit` — queries a subreddit with no data, verifies empty slice (not nil).
- `TestDeleteArticle_Exists` — deletes an existing article, verifies return value and that subsequent get returns `ErrNotFound`.
- `TestDeleteArticle_NotExists` — deletes a non-existent ID, verifies `false` return with no error.

#### Trending Repos

- `TestInsertTrendingRepo_Success` — inserts a single repo, verifies retrievability.
- `TestInsertTrendingRepos_Batch` — batch inserts 20 repos, verifies count.
- `TestListTrendingRepos_FilterByLanguage` — inserts Go and Rust repos, filters by "Go", verifies only Go repos returned.
- `TestListTrendingRepos_FilterByPeriod` — inserts repos for different periods, filters by "past_week".
- `TestListTrendingRepos_FilterByTimeRange` — inserts repos at different `fetched_at` times, filters with `FetchedAfter` / `FetchedBefore`.
- `TestListTrendingRepos_Pagination` — verifies cursor-based pagination.
- `TestDeleteTrendingRepo` — standard delete verification.

#### Lifecycle

- `TestPing` — verifies connectivity.
- `TestNewPostgresStore_InvalidConnString` — verifies error handling for a bad DSN.

### 10.3 CI Integration

Update `.github/workflows/ci.yaml` to add a Postgres service container for integration tests:

```yaml
jobs:
  backend-test:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_USER: byline_test
          POSTGRES_PASSWORD: testpass
          POSTGRES_DB: byline_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd="pg_isready -U byline_test"
          --health-interval=10s
          --health-timeout=5s
          --health-retries=5
    env:
      BYLINE_TEST_DATABASE_URL: postgres://byline_test:testpass@localhost:5432/byline_test?sslmode=disable
    steps:
      - uses: actions/checkout@v4
      - uses: jdx/mise-action@v2
        with:
          experimental: true
      - name: Run tests
        run: mise run backend:test
```

The `t.Skip` guard in the test setup ensures that if someone runs `go test ./...` locally without a Postgres instance, the integration tests are skipped rather than failing.

---

## 11. New Dependencies

Add to `go.mod`:

```
github.com/jackc/pgx/v5                  # Postgres driver + connection pool
github.com/golang-migrate/migrate/v4      # Schema migrations
```

Install commands:

```bash
cd backend && go get github.com/jackc/pgx/v5
cd backend && go get github.com/golang-migrate/migrate/v4
```

---

## 12. Implementation Sequence

This is the recommended order for implementation:

| Step | File | Description |
|------|------|-------------|
| 1 | `backend/internal/store/migrations/000001_create_reddit_articles.up.sql` | Reddit table schema |
| 2 | `backend/internal/store/migrations/000001_create_reddit_articles.down.sql` | Reddit table drop |
| 3 | `backend/internal/store/migrations/000002_create_trending_repos.up.sql` | Trending repos table schema |
| 4 | `backend/internal/store/migrations/000002_create_trending_repos.down.sql` | Trending repos table drop |
| 5 | `backend/internal/store/store.go` | Interface, types, sentinel errors, ListOptions, TrendingRepoFilter, TrendingRepoRecord |
| 6 | `backend/internal/store/postgres.go` | Full PostgresStore implementation (constructor, all CRUD methods, Ping, Close) |
| 7 | `backend/internal/store/postgres_test.go` | Integration tests for all methods |
| 8 | `.github/workflows/ci.yaml` | Add Postgres service container + env var |
| 9 | `go.mod` / `go.sum` | Updated via `go get` |

---

## 13. Future Considerations

These are not needed now but worth noting for future iterations:

- **Read replicas**: pgxpool supports `AfterConnect` hooks. A read-only pool can be created pointing to a replica endpoint. The `Store` interface methods can be split into `Reader` and `Writer` interfaces when needed.
- **Observability**: pgx supports tracing via `pgx.ConnConfig.Tracer`. OpenTelemetry integration can be added by implementing `pgx.QueryTracer`.
- **Connection string rotation**: For providers that rotate credentials (e.g., Neon branching), pgxpool's `BeforeAcquire` hook can validate and refresh connections.
- **Soft deletes**: If article history matters, add a `deleted_at TIMESTAMPTZ` column and filter on `deleted_at IS NULL` in queries rather than issuing `DELETE`.
