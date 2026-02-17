# Poller Design Document

## 1. Overview

Byline currently has two data source clients that operate on demand: `reddit.Parser` fetches and
parses Reddit RSS feeds for external article links, and `ossinsight.Client` retrieves trending
open-source repositories from the OSSInsight API. Neither client runs on a schedule today -- they
must be invoked manually or by an external trigger.

The poller is a long-running process that periodically invokes these clients on a configurable
interval, collects their results, and persists them to a PostgreSQL database via the
`backend/internal/store/` package. It is the backbone of Byline's data ingestion pipeline: without
it, there is no continuous feed of content.

Goals:

- Continuously fetch Reddit articles and OSSInsight trending repos on independent schedules.
- Persist fetched data using domain-specific store methods (`store.Store.UpsertArticles` for
  Reddit, `store.Store.InsertTrendingRepos` for OSSInsight) rather than a single generic save.
- Degrade gracefully when individual sources fail (one broken feed must not block the others).
- Shut down cleanly on SIGINT/SIGTERM, draining in-flight fetches before exiting.
- Remain testable in isolation through interface-driven design and dependency injection.


## 2. Architecture

### Where the code lives

All new poller code resides under `backend/internal/poller/`. The persistence layer lives in
`backend/internal/store/`, which provides a `Store` interface backed by PostgreSQL. Both packages
are internal, consistent with the existing layout.

```
backend/
  internal/
    reddit/
      parser.go          # existing -- reddit.Parser, reddit.Article
      parser_test.go     # existing
    ossinsight/
      client.go          # existing -- ossinsight.Client, ossinsight.Repository
      client_test.go     # existing
    store/
      store.go           # Store interface, sentinel errors, ListOptions, TrendingRepoFilter, TrendingRepoRecord
      postgres.go        # PostgresStore implementation (pgxpool-backed)
      postgres_test.go   # Integration tests against real Postgres
      migrations/
        000001_create_reddit_articles.up.sql
        000001_create_reddit_articles.down.sql
        000002_create_trending_repos.up.sql
        000002_create_trending_repos.down.sql
    poller/
      poller.go          # Poller orchestrator
      source.go          # Source interface + Reddit/OSSInsight adapters
      item.go            # FeedItem unified type (retained for downstream/API consumers)
      config.go          # PollerConfig, SourceConfig
      poller_test.go     # Unit tests
      source_test.go     # Adapter unit tests
```

### Dependency graph

```
                  +-----------+
                  |  poller   |
                  +-----+-----+
                 /      |      \
                /       |       \
  +--------+  +----------+  +-------------------+
  | reddit |  |ossinsight|  | store (interface)  |
  +--------+  +----------+  +-------------------+
                                     |
                             +-------------------+
                             | store.PostgresStore|
                             | (pgx/v5 + pgxpool)|
                             +-------------------+
```

The `poller` package imports `reddit`, `ossinsight`, and `store`. The `store.Store` interface
is defined in `backend/internal/store/store.go` and provides typed CRUD methods for each data
source. The `PostgresStore` implementation lives in `backend/internal/store/postgres.go`. The
poller receives a `store.Store` via dependency injection, so alternative implementations (such
as `LogStore` for development) can be substituted without modifying the poller core.


## 3. Core Design

### 3.1 Source interface

Each data feed is wrapped behind a common `Source` interface so the poller loop does not need to
know the specifics of Reddit vs. OSSInsight (or any future source).

```go
// Source represents a single data feed that can be polled for new items.
type Source interface {
    // Name returns a human-readable identifier for this source (used in logs).
    Name() string

    // Fetch retrieves the latest items from the source.
    // It must respect the provided context for cancellation and timeouts.
    Fetch(ctx context.Context) ([]FeedItem, error)
}
```

### 3.2 Reddit adapter

The Reddit adapter wraps `reddit.Parser` and polls one or more subreddits per invocation.

```go
type RedditSource struct {
    parser     *reddit.Parser
    subreddits []string
}

func NewRedditSource(parser *reddit.Parser, subreddits []string) *RedditSource { ... }

func (s *RedditSource) Name() string { return "reddit" }

func (s *RedditSource) Fetch(ctx context.Context) ([]FeedItem, error) {
    var items []FeedItem
    for _, sub := range s.subreddits {
        articles, err := s.parser.ParseSubreddit(ctx, sub)
        if err != nil {
            // Log and continue -- one failing subreddit should not block the rest.
            log.Printf("[reddit] error fetching r/%s: %v", sub, err)
            continue
        }
        for _, a := range articles {
            items = append(items, toFeedItem(a))
        }
    }
    return items, nil
}
```

Each call to `reddit.Parser.ParseSubreddit(ctx, subreddit)` builds the RSS URL
(`https://www.reddit.com/r/{subreddit}/.rss`) and fetches it through `gofeed`. The adapter
iterates over the configured subreddits sequentially within a single `Fetch` call. If a
subreddit fails, the error is logged but the remaining subreddits are still fetched. A total
failure (all subreddits erroring) still returns a nil error with an empty slice -- the poller
treats zero items from a successful `Fetch` as a no-op rather than an error condition.

### 3.3 OSSInsight adapter

The OSSInsight adapter wraps `ossinsight.Client` and issues one or more queries per invocation
(e.g., different language/period combinations).

```go
type OSSInsightSource struct {
    client  *ossinsight.Client
    queries []ossinsight.TrendingReposOptions
}

func NewOSSInsightSource(client *ossinsight.Client, queries []ossinsight.TrendingReposOptions) *OSSInsightSource { ... }

func (s *OSSInsightSource) Name() string { return "ossinsight" }

func (s *OSSInsightSource) Fetch(ctx context.Context) ([]FeedItem, error) {
    var items []FeedItem
    for _, q := range s.queries {
        repos, err := s.client.GetTrendingRepos(ctx, q)
        if err != nil {
            log.Printf("[ossinsight] error fetching trending (period=%s, lang=%s): %v",
                q.Period, q.Language, err)
            continue
        }
        for _, r := range repos {
            items = append(items, repoToFeedItem(r))
        }
    }
    return items, nil
}
```

Each query calls `ossinsight.Client.GetTrendingRepos(ctx, opts)` which hits
`GET /v1/trending/repos` with the configured `Period` and `Language` query parameters. The
`TrendingReposOptions` struct supports `Period` values (`past_24_hours`, `past_week`,
`past_month`, `past_3_months`) and an optional `Language` string.

### 3.4 Poller orchestrator

The `Poller` struct owns the main run loop. It manages a set of `sourceRunner` entries, each
binding a `Source` to its own ticker and running independently. The poller depends on
`store.Store` for persistence and dispatches to source-specific store methods based on the
source type.

```go
type Poller struct {
    runners []sourceRunner
    store   store.Store
    logger  *slog.Logger
}

type sourceRunner struct {
    source   Source
    interval time.Duration
}

func New(cfg Config, st store.Store, logger *slog.Logger) *Poller { ... }
```

The `Run` method starts a goroutine per source and blocks until the context is cancelled.

```go
func (p *Poller) Run(ctx context.Context) error {
    var wg sync.WaitGroup

    for _, r := range p.runners {
        wg.Add(1)
        go func(r sourceRunner) {
            defer wg.Done()
            p.runSource(ctx, r)
        }(r)
    }

    wg.Wait()
    return ctx.Err()
}

func (p *Poller) runSource(ctx context.Context, r sourceRunner) {
    // Fetch immediately on startup, then on each tick.
    p.fetchAndStore(ctx, r.source)

    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            p.logger.Info("source stopping", "source", r.source.Name())
            return
        case <-ticker.C:
            p.fetchAndStore(ctx, r.source)
        }
    }
}
```

The `fetchAndStore` method fetches items and then persists them using the appropriate
`store.Store` method based on the source name. Reddit items are persisted via
`store.Store.UpsertArticles`, which performs idempotent upserts using the
`(subreddit, article_link)` unique constraint. OSSInsight items are persisted via
`store.Store.InsertTrendingRepos`, which creates point-in-time snapshots.

```go
func (p *Poller) fetchAndStore(ctx context.Context, src Source) {
    fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    items, err := src.Fetch(fetchCtx)
    if err != nil {
        p.logger.Error("fetch failed", "source", src.Name(), "error", err)
        return
    }

    if len(items) == 0 {
        p.logger.Debug("no items returned", "source", src.Name())
        return
    }

    if err := p.persist(ctx, src, items); err != nil {
        p.logger.Error("store failed", "source", src.Name(), "error", err)
    } else {
        p.logger.Info("items stored", "source", src.Name(), "count", len(items))
    }
}

// persist dispatches to the correct store method based on the source type.
// Reddit data is upserted (idempotent); OSSInsight data is inserted as new snapshots.
func (p *Poller) persist(ctx context.Context, src Source, items []FeedItem) error {
    switch src.Name() {
    case "reddit":
        articles := make([]reddit.Article, 0, len(items))
        for _, item := range items {
            articles = append(articles, feedItemToArticle(item))
        }
        n, err := p.store.UpsertArticles(ctx, articles)
        if err != nil {
            return fmt.Errorf("upserting reddit articles: %w", err)
        }
        p.logger.Debug("reddit upsert complete", "rows_affected", n)
        return nil

    case "ossinsight":
        repos := make([]ossinsight.Repository, 0, len(items))
        // Extract the period and language from item metadata for the store call.
        var period, language string
        for _, item := range items {
            repos = append(repos, feedItemToRepository(item))
            if period == "" {
                period = item.Metadata["trending_period"]
            }
            if language == "" {
                language = item.Metadata["trending_language"]
            }
        }
        n, err := p.store.InsertTrendingRepos(ctx, repos, period, language)
        if err != nil {
            return fmt.Errorf("inserting trending repos: %w", err)
        }
        p.logger.Debug("trending repos insert complete", "rows_inserted", n)
        return nil

    default:
        return fmt.Errorf("unknown source type: %s", src.Name())
    }
}
```

An alternative design avoids the `FeedItem` round-trip for persistence entirely by having source
adapters also expose typed accessors (e.g., `RedditSource` exposes a `FetchArticles` method that
returns `[]reddit.Article` directly). Section 5 discusses this optimization in more detail.

Key design decisions:

- **One goroutine per source.** Sources have independent intervals and should not block each
  other. A slow Reddit fetch must not delay an OSSInsight poll.
- **Immediate first fetch.** The poller fires each source once at startup rather than waiting
  for the first tick. This ensures data is available immediately after launch.
- **Per-fetch timeout.** Each `Fetch` call gets its own `context.WithTimeout` derived from the
  parent context. This prevents a hung HTTP request from blocking the source forever.
- **sync.WaitGroup for shutdown.** `Run` blocks on the WaitGroup so the caller knows when all
  goroutines have fully drained.
- **Source-specific persistence.** The `persist` method dispatches to `UpsertArticles` or
  `InsertTrendingRepos` based on the source name, leveraging the store's domain-specific
  methods and deduplication strategies rather than a single generic `Save` call.


## 4. Configuration

### 4.1 Config struct

```go
type Config struct {
    // Reddit configures the Reddit source. Nil disables the source.
    Reddit *RedditConfig

    // OSSInsight configures the OSSInsight source. Nil disables the source.
    OSSInsight *OSSInsightConfig

    // FetchTimeout is the per-fetch context timeout. Defaults to 30s.
    FetchTimeout time.Duration
}

type RedditConfig struct {
    // Subreddits is the list of subreddits to poll (e.g., ["golang", "rust", "programming"]).
    Subreddits []string

    // Interval is how often to poll Reddit. Defaults to 5m.
    // Reddit rate-limits RSS feeds; intervals below 2m are not recommended.
    Interval time.Duration
}

type OSSInsightConfig struct {
    // Queries defines the set of trending repo queries to execute each cycle.
    // Each entry can specify a different Period and Language filter.
    Queries []ossinsight.TrendingReposOptions

    // Interval is how often to poll OSSInsight. Defaults to 1h.
    // Trending data updates infrequently; aggressive polling yields no benefit.
    Interval time.Duration
}
```

### 4.2 Recommended defaults

| Parameter                    | Default         | Rationale                                            |
|------------------------------|-----------------|------------------------------------------------------|
| `Reddit.Interval`           | `5m`            | Reddit RSS feeds update roughly every few minutes.   |
| `Reddit.Subreddits`         | `["golang"]`    | Start with one subreddit; expand as needed.          |
| `OSSInsight.Interval`       | `1h`            | Trending repos change slowly; hourly is sufficient.  |
| `OSSInsight.Queries`        | `[{Period: "past_24_hours"}]` | Broadest trending view without language filter. |
| `FetchTimeout`              | `30s`           | Generous enough for slow RSS feeds.                  |

### 4.3 Future: external configuration

Initially the config is built in code (e.g., in a `main.go` or a CLI command). In the future,
it can be loaded from a YAML/TOML file or environment variables. The struct-based design makes
this straightforward with libraries like `envconfig` or `koanf`.


## 5. Data Flow

### 5.1 FeedItem type

The `FeedItem` type provides a normalized view of content from any source. It remains useful for
downstream consumers such as an API layer or notification system, but is no longer the primary
type used on the persistence path. Persistence uses domain types directly (see Section 5.3).

```go
// SourceType identifies the origin of a feed item.
type SourceType string

const (
    SourceReddit     SourceType = "reddit"
    SourceOSSInsight SourceType = "ossinsight"
)

// FeedItem is the normalized representation of a piece of content
// from any supported source.
type FeedItem struct {
    // Source identifies where this item came from.
    Source SourceType

    // Title is the headline or name of the item.
    Title string

    // URL is the canonical link to the content.
    URL string

    // Description is an optional summary or description.
    Description string

    // Author is the person or entity who published/shared the item.
    Author string

    // Tags holds categorization metadata (subreddit name, programming language, etc.).
    Tags []string

    // PublishedAt is when the item was originally published or shared.
    PublishedAt time.Time

    // FetchedAt is when the poller retrieved this item.
    FetchedAt time.Time

    // Metadata holds source-specific data that does not fit the common fields.
    // For Reddit: {"subreddit": "golang", "reddit_link": "https://..."}
    // For OSSInsight: {"stars": "5000", "forks": "1200", "total_score": "6680",
    //   "trending_period": "past_24_hours", "trending_language": "Go"}
    Metadata map[string]string
}
```

### 5.2 Conversion functions

**Reddit** -- converts a `reddit.Article` to `FeedItem`:

```go
func toFeedItem(a reddit.Article) FeedItem {
    return FeedItem{
        Source:      SourceReddit,
        Title:       a.Title,
        URL:         a.ArticleLink,
        Author:      a.Author,
        Tags:        []string{a.Subreddit},
        PublishedAt: a.PostedAt,
        FetchedAt:   time.Now(),
        Metadata: map[string]string{
            "subreddit":   a.Subreddit,
            "reddit_link": a.RedditLink,
        },
    }
}
```

**OSSInsight** -- converts an `ossinsight.Repository` to `FeedItem`:

```go
func repoToFeedItem(r ossinsight.Repository) FeedItem {
    return FeedItem{
        Source:      SourceOSSInsight,
        Title:       r.RepoName,
        URL:         "https://github.com/" + r.RepoName,
        Description: r.Description,
        Tags:        []string{r.PrimaryLanguage},
        FetchedAt:   time.Now(),
        Metadata: map[string]string{
            "stars":              fmt.Sprintf("%d", r.Stars),
            "forks":             fmt.Sprintf("%d", r.Forks),
            "pull_requests":     fmt.Sprintf("%d", r.PullRequests),
            "total_score":       fmt.Sprintf("%d", r.TotalScore),
            "language":          r.PrimaryLanguage,
            "trending_period":   "", // set by the OSSInsightSource adapter
            "trending_language": "", // set by the OSSInsightSource adapter
        },
    }
}
```

**Reverse conversions** (FeedItem back to domain type, used in the `persist` method):

```go
func feedItemToArticle(item FeedItem) reddit.Article {
    return reddit.Article{
        Author:      item.Author,
        Subreddit:   item.Metadata["subreddit"],
        PostedAt:    item.PublishedAt,
        ArticleLink: item.URL,
        Title:       item.Title,
        RedditLink:  item.Metadata["reddit_link"],
    }
}

func feedItemToRepository(item FeedItem) ossinsight.Repository {
    stars, _ := strconv.Atoi(item.Metadata["stars"])
    forks, _ := strconv.Atoi(item.Metadata["forks"])
    prs, _ := strconv.Atoi(item.Metadata["pull_requests"])
    score, _ := strconv.Atoi(item.Metadata["total_score"])
    return ossinsight.Repository{
        RepoName:        item.Title,
        PrimaryLanguage: item.Metadata["language"],
        Description:     item.Description,
        Stars:           stars,
        Forks:           forks,
        PullRequests:    prs,
        TotalScore:      score,
    }
}
```

### 5.3 Persistence path: domain types directly

While the `Source` interface returns `[]FeedItem` for generality, the persistence path avoids
lossy round-trips by converting back to the original domain types before calling the store.
The `persist` method in the poller (see Section 3.4) dispatches based on source name:

- **Reddit**: `[]FeedItem` -> `feedItemToArticle()` -> `[]reddit.Article` -> `store.UpsertArticles(ctx, articles)`
- **OSSInsight**: `[]FeedItem` -> `feedItemToRepository()` -> `[]ossinsight.Repository` -> `store.InsertTrendingRepos(ctx, repos, period, language)`

An alternative (and potentially cleaner) approach is to have the source adapters expose typed
fetch methods alongside the generic `Source` interface. For example, `RedditSource` could
implement an additional `FetchArticles(ctx) ([]reddit.Article, error)` method that the poller
uses directly for persistence, bypassing the `FeedItem` conversion entirely. This optimization
can be applied in a later refactoring pass if the `FeedItem` round-trip introduces friction.

### 5.4 End-to-end flow

```
 Ticker fires (per-source goroutine)
      |
      v
 Source.Fetch(ctx)
      |
      +-- [reddit source] -------------------------+
      |   reddit.Parser.ParseSubreddit(ctx, "golang")
      |       |
      |       v
      |   []reddit.Article
      |       |
      |       v
      |   toFeedItem() for each article
      |       |                                     |
      |       v                                     |
      |   []FeedItem                                |
      |       |                                     |
      +-- [ossinsight source] ---------------------+
      |   ossinsight.Client.GetTrendingRepos(ctx, opts)
      |       |
      |       v
      |   []ossinsight.Repository
      |       |
      |       v
      |   repoToFeedItem() for each repo
      |       |
      |       v
      |   []FeedItem
      |
      v
 persist(ctx, source, items)
      |
      +-- source.Name() == "reddit":
      |       feedItemToArticle() for each item
      |       |
      |       v
      |   store.UpsertArticles(ctx, []reddit.Article)
      |       |
      |       v
      |   INSERT ... ON CONFLICT (subreddit, article_link) DO UPDATE
      |
      +-- source.Name() == "ossinsight":
              feedItemToRepository() for each item
              |
              v
          store.InsertTrendingRepos(ctx, []ossinsight.Repository, period, language)
              |
              v
          INSERT into trending_repos (point-in-time snapshot)
```


## 6. Error Handling and Resilience

### 6.1 Per-subreddit / per-query isolation

Inside `RedditSource.Fetch`, each subreddit is fetched independently. If `r/golang` succeeds but
`r/rust` times out, the items from `r/golang` are still returned. The same applies to multiple
`TrendingReposOptions` queries in `OSSInsightSource.Fetch`. This is the most important resilience
property: a single broken feed never poisons the entire poll cycle.

### 6.2 Fetch-level error handling

If `Source.Fetch` itself returns an error (not just partial failures within), the poller logs the
error and moves on. The source will be retried automatically on the next tick. There is no
explicit retry loop within a single cycle -- the ticker-based design provides natural retries.

### 6.3 Exponential backoff (future enhancement)

For the initial implementation, the fixed-interval ticker provides sufficient retry behavior.
If a source fails repeatedly, it will be retried every `Interval`. A future enhancement can
layer exponential backoff on top:

```go
type backoff struct {
    base    time.Duration
    max     time.Duration
    current time.Duration
}

func (b *backoff) next() time.Duration {
    d := b.current
    b.current = min(b.current*2, b.max)
    return d
}

func (b *backoff) reset() {
    b.current = b.base
}
```

On each failed fetch, the next tick is delayed by `backoff.next()`. On success, `backoff.reset()`
restores the original interval. This prevents hammering a down endpoint.

### 6.4 Structured logging

All log messages include the source name, the operation being performed, and any relevant
parameters. Use `log/slog` (standard library, available in Go 1.21+) with structured fields:

```go
p.logger.Error("fetch failed",
    "source", src.Name(),
    "error", err,
    "duration", elapsed,
)
```

### 6.5 Store failures

If `store.UpsertArticles` or `store.InsertTrendingRepos` fails, the items from that cycle are
lost. The poller logs the error and continues. The store's sentinel errors (`store.ErrNotFound`,
`store.ErrDuplicateKey`) provide structured error classification, but for batch persistence the
most common failure mode is a transient database connectivity issue. A future enhancement could
add a write-ahead buffer or dead-letter file for items that fail to persist.


## 7. Concurrency Model

### 7.1 Goroutine layout

```
main goroutine
      |
      +-- signal.NotifyContext(ctx, SIGINT, SIGTERM)
      |
      v
  Poller.Run(ctx)
      |
      +-- goroutine: runSource(ctx, redditRunner)
      |       |
      |       +-- ticker loop, calls Fetch + persist per tick
      |
      +-- goroutine: runSource(ctx, ossinsightRunner)
      |       |
      |       +-- ticker loop, calls Fetch + persist per tick
      |
      v
  wg.Wait()  <-- blocks until all goroutines exit
```

### 7.2 Context cancellation

The top-level context is created from `signal.NotifyContext` in `main`. When a SIGINT or SIGTERM
arrives, the context is cancelled. Each `runSource` goroutine checks `ctx.Done()` in its select
loop and exits. The per-fetch `context.WithTimeout` is derived from the same parent context, so
an in-flight HTTP request is also cancelled promptly.

### 7.3 Graceful shutdown sequence

1. Signal received (SIGINT/SIGTERM).
2. `signal.NotifyContext` cancels `ctx`.
3. Each `runSource` goroutine's `select` picks up `<-ctx.Done()` and returns.
4. Any in-flight `Fetch` call receives context cancellation through its derived timeout context.
   The underlying `http.Client` or `gofeed.Parser` aborts the HTTP request.
5. `wg.Wait()` in `Run` unblocks once all goroutines have returned.
6. `Run` returns `ctx.Err()` (typically `context.Canceled`).
7. `main` closes the `store.Store` (releasing the database connection pool) and exits cleanly.

### 7.4 Thread safety

- Each source goroutine operates independently; no shared mutable state between sources.
- `reddit.Parser` uses `gofeed.Parser` internally, which creates a new HTTP request per call
  and is safe for concurrent use.
- `ossinsight.Client` uses `http.Client`, which is safe for concurrent use.
- The `store.Store` implementation (`PostgresStore`) uses `pgxpool.Pool`, which is safe for
  concurrent use. Multiple source goroutines may call `UpsertArticles` and `InsertTrendingRepos`
  concurrently without external synchronization.

### 7.5 Entry point sketch

```go
// cmd/poller/main.go (or integrated into an existing main)
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
        Level: slog.LevelInfo,
    }))

    // --- Database setup ---
    databaseURL := os.Getenv("DATABASE_URL")
    if databaseURL == "" {
        logger.Error("DATABASE_URL environment variable is required")
        os.Exit(1)
    }

    // Run migrations before opening the connection pool.
    if err := store.RunMigrations(databaseURL, "backend/internal/store/migrations"); err != nil {
        logger.Error("failed to run database migrations", "error", err)
        os.Exit(1)
    }

    // Create the PostgresStore (connection pool).
    pgStore, err := store.NewPostgresStore(ctx, databaseURL)
    if err != nil {
        logger.Error("failed to connect to database", "error", err)
        os.Exit(1)
    }
    defer pgStore.Close()

    // --- Poller configuration ---
    cfg := poller.Config{
        Reddit: &poller.RedditConfig{
            Subreddits: []string{"golang", "rust", "programming"},
            Interval:   5 * time.Minute,
        },
        OSSInsight: &poller.OSSInsightConfig{
            Queries: []ossinsight.TrendingReposOptions{
                {Period: ossinsight.PeriodPast24Hours},
                {Period: ossinsight.PeriodPast24Hours, Language: "Go"},
            },
            Interval: 1 * time.Hour,
        },
        FetchTimeout: 30 * time.Second,
    }

    p := poller.New(cfg, pgStore, logger)

    logger.Info("poller starting")
    if err := p.Run(ctx); err != nil && err != context.Canceled {
        logger.Error("poller exited with error", "error", err)
        os.Exit(1)
    }
    logger.Info("poller shut down cleanly")
}
```


## 8. Storage

### 8.1 Production implementation: PostgresStore

The `store.PostgresStore` is the production persistence layer, implemented using `pgx/v5` with
`pgxpool` for connection pooling. It is defined in `backend/internal/store/postgres.go` and
implements the full `store.Store` interface.

Key characteristics:

- **Connection pooling** via `pgxpool.Pool` with sensible defaults (max 10 conns, min 2, 30min
  lifetime, 5min idle time, 30s health checks).
- **Provider-agnostic**: accepts a standard `postgres://` connection string that works with
  Neon, AWS RDS, Supabase, local Docker, or any other Postgres provider.
- **Upserts for Reddit articles**: `UpsertArticles` uses `INSERT ... ON CONFLICT (subreddit, article_link) DO UPDATE`,
  making repeated scrapes of the same feed idempotent. The same article link in the same
  subreddit is updated rather than duplicated.
- **Point-in-time inserts for trending repos**: `InsertTrendingRepos` creates a new snapshot row
  for each fetch. A unique constraint on `(repo_id, trending_period, trending_language, fetched_at)`
  prevents duplicate inserts from the same scrape run while preserving historical data for
  trend analysis.
- **Batch operations**: Both `UpsertArticles` and `InsertTrendingRepos` use pgx batch/pipeline
  within a single transaction for efficient bulk writes (25+ items per call).
- **Cursor-based pagination**: `ListArticles` and `ListTrendingRepos` support cursor-based
  pagination via `ListOptions`, which is O(1) regardless of page depth.
- **Sentinel errors**: `store.ErrNotFound` and `store.ErrDuplicateKey` provide structured
  error classification. Internal pgx errors are translated via a `classifyError` helper that
  maps Postgres error code `23505` (unique_violation) to `ErrDuplicateKey`.

### 8.2 Schema migrations

The database schema is managed by `golang-migrate/migrate/v4` with plain SQL migration files in
`backend/internal/store/migrations/`. Migrations are run at application startup before the
connection pool is created (see Section 7.5). For production deployments, migration files are
embedded in the binary via `embed.FS`.

Migration files:
- `000001_create_reddit_articles.up.sql` / `down.sql` -- `reddit_articles` table with indexes
  on `(subreddit, posted_at DESC)` and `(article_link)`.
- `000002_create_trending_repos.up.sql` / `down.sql` -- `trending_repos` table with indexes
  on `(trending_language, trending_period, fetched_at DESC)`, `(repo_id)`, and `(total_score DESC)`.

### 8.3 Development/testing: LogStore

For local development and testing without a database, the `LogStore` provides a no-dependency
implementation that logs each item via `slog`. Since the poller now calls source-specific store
methods (`UpsertArticles`, `InsertTrendingRepos`), a development `LogStore` must implement the
full `store.Store` interface, logging the incoming data and returning success:

```go
type LogStore struct {
    logger *slog.Logger
}

func NewLogStore(logger *slog.Logger) *LogStore {
    return &LogStore{logger: logger}
}

func (s *LogStore) UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error) {
    for _, a := range articles {
        s.logger.Info("feed item",
            "source", "reddit",
            "title", a.Title,
            "url", a.ArticleLink,
        )
    }
    return int64(len(articles)), nil
}

func (s *LogStore) InsertTrendingRepos(ctx context.Context, repos []ossinsight.Repository, period, language string) (int64, error) {
    for _, r := range repos {
        s.logger.Info("feed item",
            "source", "ossinsight",
            "title", r.RepoName,
            "url", "https://github.com/"+r.RepoName,
        )
    }
    return int64(len(repos)), nil
}

// Remaining store.Store methods return no-ops or ErrNotFound as appropriate.
```

`LogStore` is useful for:
- Running the poller locally without standing up a Postgres instance.
- Unit testing the poller orchestrator without database dependencies.
- Quick iteration during development of new source adapters.

For production use, always use `PostgresStore`.

### 8.4 Deduplication strategy

Deduplication is handled at the database level, not in application code:

| Source     | Strategy | Mechanism |
|------------|----------|-----------|
| Reddit     | Upsert   | `UNIQUE(subreddit, article_link)` constraint + `ON CONFLICT DO UPDATE`. Re-scraping the same feed updates existing rows rather than duplicating them. |
| OSSInsight | Unique constraint | `UNIQUE(repo_id, trending_period, trending_language, fetched_at)` constraint. Each scrape run creates new snapshot rows; duplicate inserts from the same run are rejected. |

This approach is simpler and more reliable than in-memory deduplication (which would be lost on
restart) and avoids the need for pre-check queries before inserts.


## 9. Testing Strategy

### 9.1 Unit tests for Source adapters

Create mock/stub implementations that return canned data without hitting the network. The existing
codebase already demonstrates this pattern: `reddit.Parser.ParseString` accepts raw feed XML, and
the OSSInsight tests use `httptest.NewServer` to provide fake HTTP responses.

**RedditSource tests:**

```go
func TestRedditSource_Fetch(t *testing.T) {
    // Use a mock parser or ParseString-based approach.
    // Verify that returned FeedItems have Source == SourceReddit,
    // correct URL/Title/Author mapping, and that self-posts are excluded.
}

func TestRedditSource_PartialFailure(t *testing.T) {
    // Configure two subreddits; make one fail.
    // Verify the other subreddit's items are still returned.
}
```

**OSSInsightSource tests:**

```go
func TestOSSInsightSource_Fetch(t *testing.T) {
    // Use httptest.NewServer returning canned JSON (same pattern as client_test.go).
    // Verify FeedItems have Source == SourceOSSInsight, correct metadata mapping.
}
```

### 9.2 Unit tests for the Poller orchestrator

Test the poller with fake `Source` and a `LogStore` (or a minimal `store.Store` stub).

```go
// fakeSource implements Source with controllable behavior.
type fakeSource struct {
    name  string
    items []FeedItem
    err   error
    calls int
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Fetch(ctx context.Context) ([]FeedItem, error) {
    f.calls++
    return f.items, f.err
}

// fakeStore implements store.Store and records calls.
type fakeStore struct {
    mu             sync.Mutex
    articleCalls   int
    articles       []reddit.Article
    repoCalls      int
    repos          []ossinsight.Repository
}

func (f *fakeStore) UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.articleCalls++
    f.articles = append(f.articles, articles...)
    return int64(len(articles)), nil
}

func (f *fakeStore) InsertTrendingRepos(ctx context.Context, repos []ossinsight.Repository, period, language string) (int64, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.repoCalls++
    f.repos = append(f.repos, repos...)
    return int64(len(repos)), nil
}

// ... remaining store.Store methods as no-ops ...
```

Test cases:

- **Happy path.** Start poller with a short interval (10ms), let it tick twice, cancel context,
  verify store received items from both ticks.
- **Source error.** Source returns an error; verify the poller logs it and continues ticking.
- **Store error.** Store returns an error; verify the poller logs it, does not crash, and
  retries on the next tick.
- **Context cancellation.** Cancel context immediately; verify `Run` returns promptly without
  blocking.
- **Multiple sources.** Register two sources with different intervals; verify both produce items
  independently and that Reddit items go through `UpsertArticles` while OSSInsight items go
  through `InsertTrendingRepos`.

### 9.3 Integration tests (network)

Integration tests hit the real Reddit RSS and OSSInsight APIs. These are gated behind a build
tag so they do not run in CI by default.

```go
//go:build integration

func TestRedditSource_Integration(t *testing.T) {
    parser := reddit.NewParser()
    src := NewRedditSource(parser, []string{"golang"})

    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()

    items, err := src.Fetch(ctx)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if len(items) == 0 {
        t.Log("warning: no items returned (feed may be empty)")
    }

    for _, item := range items {
        if item.Source != SourceReddit {
            t.Errorf("expected source reddit, got %s", item.Source)
        }
        if item.URL == "" {
            t.Error("expected non-empty URL")
        }
    }
}
```

Run integration tests with:
```
cd backend && go test -v -tags=integration ./internal/poller/
```

### 9.4 Store integration tests (database)

The `store` package has its own integration tests in `backend/internal/store/postgres_test.go`
that run against a real PostgreSQL instance. These tests are gated behind the
`BYLINE_TEST_DATABASE_URL` environment variable -- if the variable is not set, the tests are
skipped via `t.Skip`, allowing `go test ./...` to pass in environments without Postgres.

To run store integration tests locally:

```bash
# Start a local Postgres (e.g., via Docker)
docker run -d --name byline-test-pg -p 5432:5432 \
    -e POSTGRES_USER=byline_test \
    -e POSTGRES_PASSWORD=testpass \
    -e POSTGRES_DB=byline_test \
    postgres:16

# Run the tests
BYLINE_TEST_DATABASE_URL="postgres://byline_test:testpass@localhost:5432/byline_test?sslmode=disable" \
    go test -v ./backend/internal/store/...
```

In CI, a Postgres service container is configured in `.github/workflows/ci.yaml` to provide the
test database automatically:

```yaml
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
```

The test setup creates a `PostgresStore`, runs migrations, and truncates tables between tests
for isolation. See `postgres-crud-implementation.md` for the full list of store test cases
covering upsert idempotency, batch operations, cursor pagination, filtering, and error handling.

### 9.5 Test coverage target

Aim for 85%+ coverage on the `poller` package. The `Source` interface and `store.Store` interface
make it straightforward to test all code paths without network or database access. The `store`
package targets similar coverage via its own integration test suite.


## 10. Implementation Phases

### Phase 1: Core types and interfaces

**Files:** `poller/item.go`, `poller/source.go`, `poller/config.go`

Deliverables:
- Define `FeedItem` struct. Note: `FeedItem` serves downstream consumers (API, notifications)
  and the `Source` interface contract. The persistence path uses domain types directly
  (`reddit.Article`, `ossinsight.Repository`), so `FeedItem` is not on the critical write path.
- Define `Source` interface.
- Define `Config`, `RedditConfig`, `OSSInsightConfig` structs.
- Implement `toFeedItem(reddit.Article) FeedItem` conversion.
- Implement `repoToFeedItem(ossinsight.Repository) FeedItem` conversion.
- Implement `feedItemToArticle(FeedItem) reddit.Article` reverse conversion.
- Implement `feedItemToRepository(FeedItem) ossinsight.Repository` reverse conversion.
- Write unit tests for conversion functions (both directions).

No goroutines yet. This phase establishes the data contracts.

### Phase 2: Store package

**Files:** `store/store.go`, `store/postgres.go`, `store/postgres_test.go`, `store/migrations/*.sql`

This phase implements the persistence layer as defined in `postgres-crud-implementation.md`:
- Define the `store.Store` interface with typed CRUD methods for Reddit articles and trending repos.
- Define `ListOptions`, `TrendingRepoFilter`, `TrendingRepoRecord` types.
- Define sentinel errors (`ErrNotFound`, `ErrDuplicateKey`).
- Write migration SQL files for `reddit_articles` and `trending_repos` tables.
- Implement `PostgresStore` with `pgx/v5` and `pgxpool` (upserts, batch operations, cursor pagination).
- Implement `RunMigrations` using `golang-migrate/migrate/v4`.
- Write integration tests gated behind `BYLINE_TEST_DATABASE_URL`.
- Add `pgx/v5` and `golang-migrate/migrate/v4` to `go.mod`.

### Phase 3: Source adapters

**Files:** `poller/source.go` (extend), `poller/source_test.go`

Deliverables:
- Implement `RedditSource` wrapping `reddit.Parser`.
- Implement `OSSInsightSource` wrapping `ossinsight.Client`.
- Write unit tests using `reddit.Parser.ParseString` and `httptest.NewServer`.
- Verify partial failure isolation (one subreddit/query fails, others succeed).

### Phase 4: Poller orchestrator

**Files:** `poller/poller.go`, `poller/poller_test.go`

Deliverables:
- Implement `Poller` struct with `New(cfg, store.Store, logger)` constructor.
- Implement `Run(ctx)` with per-source goroutines and ticker loops.
- Implement `fetchAndStore` with per-fetch timeouts via `context.WithTimeout`.
- Implement `persist` method that dispatches to `store.UpsertArticles` or
  `store.InsertTrendingRepos` based on source name.
- Implement `LogStore` as a development/test implementation of `store.Store`.
- Write unit tests using `fakeSource` and `fakeStore` (or `LogStore`).
- Test graceful shutdown via context cancellation.

### Phase 5: Entry point and manual testing

**Files:** `cmd/poller/main.go` (new)

Deliverables:
- Create a `main.go` that reads `DATABASE_URL` from the environment.
- Run database migrations at startup via `store.RunMigrations`.
- Create a `store.NewPostgresStore` and pass it to the poller.
- Wire up config, sources, and poller.
- Handle SIGINT/SIGTERM via `signal.NotifyContext`.
- Close the `PostgresStore` on shutdown (releases connection pool).
- Add a mise task for running the poller (e.g., `mise run backend:poller`).
- Manual end-to-end test: run the poller against a local Postgres, observe items being persisted,
  send SIGINT, confirm clean shutdown.

### Phase 6: Hardening

Deliverables:
- Add exponential backoff for repeatedly failing sources.
- Add metrics/counters (items fetched, errors, fetch duration) -- can start with simple
  `slog` structured fields; Prometheus exposition can come later.
- Write integration tests gated behind `//go:build integration` for network-hitting source tests.
- Update `.github/workflows/ci.yaml` with Postgres service container for store tests.
- Update `CLAUDE.md` with new architecture notes and commands.

---

This plan keeps each phase small and independently testable. Phases 1-4 can be completed and
merged without a runnable binary. Phase 5 adds the entry point with full database integration.
Phase 6 adds production hardening. Persistent storage is no longer a "future" concern -- it is
built into the core design from Phase 2 via the `store.PostgresStore` implementation.
