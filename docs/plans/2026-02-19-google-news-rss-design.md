# Google News RSS Source Design

**Date:** 2026-02-19
**Branch:** feature/google-rss
**Feed URL:** https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en

## Overview

Add a Google News RSS parser as a new content source for Byline. The parser fetches the Google News Top Stories RSS feed, resolves Google redirect URLs to actual article URLs, and persists articles to a dedicated `news_articles` table.

## Data Model

### `googlenews.Article` struct

```go
type Article struct {
    Title       string
    URL         string    // Resolved actual article URL
    Source      string    // Publisher name (e.g. "The New York Times")
    SourceURL   string    // Publisher URL (e.g. "https://www.nytimes.com")
    PublishedAt time.Time
}
```

### Database table `news_articles`

```sql
CREATE TABLE news_articles (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title        TEXT        NOT NULL,
    article_url  TEXT        NOT NULL,
    source_name  TEXT        NOT NULL,
    source_url   TEXT        NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_news_articles_url UNIQUE (article_url)
);
CREATE INDEX idx_news_articles_published ON news_articles (published_at DESC);
CREATE INDEX idx_news_articles_source ON news_articles (source_name);
```

Deduplication key: `article_url` (the resolved URL). Upsert on conflict updates title and timestamps.

## Parser

New package: `internal/googlenews/`

`googlenews.Parser` wraps `gofeed.Parser` and an `http.Client`:

- `NewParser()` creates the parser with an HTTP client configured for redirect resolution.
- `ParseFeed(ctx, feedURL)` fetches the RSS feed, extracts items, resolves redirect URLs for each item.
- `ParseString(feedContent)` parses from string for testing (no redirect resolution).
- `resolveURL(ctx, googleURL)` performs an HTTP HEAD request following redirects to get the final article URL (5s per-request timeout).

Each RSS item yields one article using the primary title, resolved link, `<source>` publisher info, and `pubDate`. The HTML description clusters are ignored.

If redirect resolution fails for an article, the article is skipped (not stored with a Google redirect URL).

## Poller Integration

### Source adapter

`GoogleNewsSource` in `poller/source.go`:
- `Name()` returns `"googlenews"`
- `Fetch(ctx)` calls `parser.ParseFeed(ctx, feedURL)` and converts to `[]FeedItem`

### FeedItem conversion

New constant: `SourceGoogleNews SourceType = "googlenews"`

`newsToFeedItem(article)`:
- `Source`: `SourceGoogleNews`
- `Title`: article title
- `URL`: resolved article URL
- `Author`: empty
- `Tags`: `[]string{article.Source}` (publisher name)
- `Metadata`: `{"source_name": ..., "source_url": ...}`

Reverse: `feedItemToNewsArticle(item)` for persistence.

### Persist routing

New case in `Poller.persist()`:
- `"googlenews"` -> convert FeedItems to `[]googlenews.Article` -> `store.UpsertNewsArticles(ctx, articles)`

### Store interface additions

```go
UpsertNewsArticle(ctx context.Context, article googlenews.Article) (int64, error)
UpsertNewsArticles(ctx context.Context, articles []googlenews.Article) (int64, error)
GetNewsArticleByID(ctx context.Context, id int64) (*googlenews.Article, error)
ListNewsArticles(ctx context.Context, opts ListOptions) ([]googlenews.Article, error)
DeleteNewsArticle(ctx context.Context, id int64) (bool, error)
```

### Config

```go
type GoogleNewsConfig struct {
    FeedURL  string        // defaults to top stories URL
    Interval time.Duration // 30 minutes
}
```

## Testing

### Unit tests (`googlenews/parser_test.go`)
- Embedded sample RSS XML fixture
- `ParseString()` field extraction (title, source name, source URL, pubDate)
- Empty feed and malformed XML
- `ParseFeed()` with `httptest.Server` for redirect resolution
- Redirect timeout/failure skips the article

### Poller tests
- `newsToFeedItem` and `feedItemToNewsArticle` conversion tests
- `persist()` routing via `recordingStore`

### Store integration tests (gated behind `BYLINE_TEST_DATABASE_URL`)
- `UpsertNewsArticles` insert + dedup on same URL
- `ListNewsArticles` cursor pagination
- `GetNewsArticleByID` / `DeleteNewsArticle`

## Polling

Interval: 30 minutes. Configurable via `GoogleNewsConfig.Interval`.

## Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Link handling | Resolve Google redirects to actual URLs | Cleaner data, better deduplication |
| Description clusters | Ignore, use primary article only | Simpler, clusters add complexity for marginal value |
| Storage | Dedicated `news_articles` table | Follows existing pattern, clean separation |
| Dedup key | Resolved article URL | Natural unique identifier for articles |
| On redirect failure | Skip article | Keeps data clean, avoids storing redirect URLs |
| Package structure | Dedicated `internal/googlenews/` | Mirrors `internal/reddit/`, testable in isolation |
