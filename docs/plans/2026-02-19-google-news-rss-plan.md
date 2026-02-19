# Google News RSS Source Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a Google News RSS parser that fetches top stories, resolves redirect URLs, and persists articles to a dedicated `news_articles` table.

**Architecture:** New `internal/googlenews/` package with Parser (wraps gofeed + http.Client for redirect resolution). Integrates via the existing Source interface in the poller, with a new `news_articles` DB table and corresponding Store methods.

**Tech Stack:** Go, gofeed, pgx/v5, golang-migrate, httptest

---

### Task 1: Google News Parser — Article struct and ParseString

**Files:**
- Create: `backend/internal/googlenews/parser.go`
- Create: `backend/internal/googlenews/parser_test.go`

**Step 1: Write the failing tests**

Create `backend/internal/googlenews/parser_test.go`:

```go
package googlenews

import (
	"testing"
	"time"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <link>https://news.google.com</link>
    <description>Google News</description>
    <item>
      <title>Breaking: Major Event Unfolds - The New York Times</title>
      <link>https://news.google.com/rss/articles/CBMiXX123</link>
      <guid isPermaLink="false">CBMiXX123</guid>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <description>&lt;ol&gt;&lt;li&gt;cluster content&lt;/li&gt;&lt;/ol&gt;</description>
      <source url="https://www.nytimes.com">The New York Times</source>
    </item>
    <item>
      <title>Tech Company Announces Product - Reuters</title>
      <link>https://news.google.com/rss/articles/CBMiYY456</link>
      <guid isPermaLink="false">CBMiYY456</guid>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <description>&lt;ol&gt;&lt;li&gt;cluster content&lt;/li&gt;&lt;/ol&gt;</description>
      <source url="https://www.reuters.com">Reuters</source>
    </item>
    <item>
      <title>Item Without Source Element</title>
      <link>https://news.google.com/rss/articles/CBMiZZ789</link>
      <pubDate>Wed, 19 Feb 2026 08:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

func TestParseString(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	// All 3 items should be returned (including one without source).
	if len(articles) != 3 {
		t.Fatalf("Expected 3 articles, got %d", len(articles))
	}
}

func TestParseString_ExtractsFields(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) < 2 {
		t.Fatal("Expected at least 2 articles")
	}

	first := articles[0]
	if first.Title != "Breaking: Major Event Unfolds - The New York Times" {
		t.Errorf("Title = %q, want %q", first.Title, "Breaking: Major Event Unfolds - The New York Times")
	}
	// ParseString does not resolve redirects; URL is stored as-is.
	if first.URL != "https://news.google.com/rss/articles/CBMiXX123" {
		t.Errorf("URL = %q, want %q", first.URL, "https://news.google.com/rss/articles/CBMiXX123")
	}
	if first.Source != "The New York Times" {
		t.Errorf("Source = %q, want %q", first.Source, "The New York Times")
	}
	if first.SourceURL != "https://www.nytimes.com" {
		t.Errorf("SourceURL = %q, want %q", first.SourceURL, "https://www.nytimes.com")
	}

	expectedTime := time.Date(2026, 2, 19, 10, 30, 0, 0, time.UTC)
	if !first.PublishedAt.Equal(expectedTime) {
		t.Errorf("PublishedAt = %v, want %v", first.PublishedAt, expectedTime)
	}

	// Second article.
	second := articles[1]
	if second.Source != "Reuters" {
		t.Errorf("Source = %q, want %q", second.Source, "Reuters")
	}
	if second.SourceURL != "https://www.reuters.com" {
		t.Errorf("SourceURL = %q, want %q", second.SourceURL, "https://www.reuters.com")
	}
}

func TestParseString_ItemWithoutSource(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	// Third item has no <source> element.
	third := articles[2]
	if third.Source != "" {
		t.Errorf("Source = %q, want empty string", third.Source)
	}
	if third.SourceURL != "" {
		t.Errorf("SourceURL = %q, want empty string", third.SourceURL)
	}
}

func TestParseString_EmptyFeed(t *testing.T) {
	p := NewParser()

	emptyFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
  </channel>
</rss>`

	articles, err := p.ParseString(emptyFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if len(articles) != 0 {
		t.Errorf("Expected 0 articles, got %d", len(articles))
	}
}

func TestParseString_InvalidFeed(t *testing.T) {
	p := NewParser()

	_, err := p.ParseString("not valid xml")
	if err == nil {
		t.Error("Expected error for invalid feed, got nil")
	}
}

func TestNewParser(t *testing.T) {
	p := NewParser()
	if p == nil {
		t.Fatal("NewParser() returned nil")
	}
	if p.feedParser == nil {
		t.Error("feedParser is nil")
	}
	if p.httpClient == nil {
		t.Error("httpClient is nil")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd backend && go test -v -run "Test(ParseString|NewParser)" ./internal/googlenews/`
Expected: Compilation error — package does not exist yet.

**Step 3: Write the implementation**

Create `backend/internal/googlenews/parser.go`:

```go
package googlenews

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/extensions"
)

// Article represents a news article from Google News.
type Article struct {
	// Title is the headline of the article.
	Title string

	// URL is the resolved URL to the actual article (not the Google redirect).
	URL string

	// Source is the publisher name (e.g. "The New York Times").
	Source string

	// SourceURL is the publisher's website URL (e.g. "https://www.nytimes.com").
	SourceURL string

	// PublishedAt is the time the article was published.
	PublishedAt time.Time
}

// Parser parses Google News RSS feeds to extract news articles.
type Parser struct {
	feedParser *gofeed.Parser
	httpClient *http.Client
}

// NewParser creates a new Google News RSS feed parser.
func NewParser() *Parser {
	fp := gofeed.NewParser()
	fp.UserAgent = "byline/1.0 (RSS Feed Parser)"

	return &Parser{
		feedParser: fp,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// ParseString parses a Google News RSS feed from a string.
// Redirect resolution is skipped; URLs are stored as-is from the feed.
// This is useful for testing.
func (p *Parser) ParseString(feedContent string) ([]Article, error) {
	feed, err := p.feedParser.ParseString(feedContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse feed content: %w", err)
	}

	return p.extractArticles(feed), nil
}

// extractArticles processes feed items into Articles.
func (p *Parser) extractArticles(feed *gofeed.Feed) []Article {
	var articles []Article
	for _, item := range feed.Items {
		articles = append(articles, p.parseItem(item))
	}
	return articles
}

// parseItem extracts article information from a single feed item.
func (p *Parser) parseItem(item *gofeed.Item) Article {
	a := Article{
		Title: item.Title,
		URL:   item.Link,
	}

	// Extract publisher info from the <source> element.
	// gofeed exposes the <source> element via item.Extensions[""]["source"].
	a.Source, a.SourceURL = extractSource(item)

	// Extract published time.
	if item.PublishedParsed != nil {
		a.PublishedAt = *item.PublishedParsed
	}

	return a
}

// extractSource extracts the publisher name and URL from the RSS <source> element.
// gofeed parses <source url="...">Name</source> into the extensions map.
func extractSource(item *gofeed.Item) (name, url string) {
	// gofeed stores RSS <source> in item.Extensions under the empty namespace.
	if item.Extensions == nil {
		return "", ""
	}
	sourceExts, ok := item.Extensions[""]
	if !ok {
		return "", ""
	}
	sources, ok := sourceExts["source"]
	if !ok || len(sources) == 0 {
		return "", ""
	}
	src := sources[0]
	name = src.Value
	if urlAttr, ok := src.Attrs["url"]; ok {
		url = urlAttr
	}
	return name, url
}
```

> **Important note for the implementer:** The `extractSource` function accesses `gofeed`'s `item.Extensions[""]["source"]` to get the `<source>` element data. The gofeed library stores RSS elements that don't map to its standard `Item` struct fields in `Extensions` as `map[string]map[string][]extensions.Extension`. The empty string key `""` is the default namespace. If this doesn't work at runtime, check gofeed's actual behavior — you may need to look at `item.Custom` or use `gofeed.extensions` differently. The test will catch any mismatch.

**Step 4: Run the tests**

Run: `cd backend && go test -v -run "Test(ParseString|NewParser)" ./internal/googlenews/`
Expected: All tests PASS. If `extractSource` doesn't find the source data, fix the extraction path based on what gofeed actually puts in `item.Extensions`.

**Step 5: Commit**

```bash
cd backend && git add internal/googlenews/parser.go internal/googlenews/parser_test.go
git commit -m "feat(googlenews): add parser with Article struct and ParseString"
```

---

### Task 2: Google News Parser — ParseFeed with redirect resolution

**Files:**
- Modify: `backend/internal/googlenews/parser.go`
- Modify: `backend/internal/googlenews/parser_test.go`

**Step 1: Write the failing tests**

Append to `backend/internal/googlenews/parser_test.go`:

```go
import (
	"context"
	"net/http"
	"net/http/httptest"
	// ... existing imports
)

func TestParseFeed_ResolvesRedirects(t *testing.T) {
	// Set up a test server that:
	// 1. Serves the RSS feed at /rss
	// 2. Redirects /rss/articles/CBMiXX123 -> /resolved-article-1
	// 3. Redirects /rss/articles/CBMiYY456 -> /resolved-article-2
	// 4. Serves a 200 at /resolved-article-*
	mux := http.NewServeMux()

	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		// Serve feed with links pointing to this test server.
		feed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <item>
      <title>Article One - Publisher A</title>
      <link>` + "BASEURL" + `/rss/articles/CBMiXX123</link>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <source url="https://www.publishera.com">Publisher A</source>
    </item>
    <item>
      <title>Article Two - Publisher B</title>
      <link>` + "BASEURL" + `/rss/articles/CBMiYY456</link>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <source url="https://www.publisherb.com">Publisher B</source>
    </item>
  </channel>
</rss>`
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(feed))
	})

	mux.HandleFunc("/rss/articles/CBMiXX123", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/resolved-article-1", http.StatusFound)
	})

	mux.HandleFunc("/rss/articles/CBMiYY456", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/resolved-article-2", http.StatusFound)
	})

	mux.HandleFunc("/resolved-article-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/resolved-article-2", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// We need to fix the BASEURL in the feed handler. Instead, let's use a
	// simpler approach: create the server first, then set up a handler that
	// knows the base URL.
	// Recreate with proper URL substitution.
	srv.Close()

	var srvURL string
	mux2 := http.NewServeMux()

	mux2.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <item>
      <title>Article One - Publisher A</title>
      <link>%s/rss/articles/CBMiXX123</link>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <source url="https://www.publishera.com">Publisher A</source>
    </item>
    <item>
      <title>Article Two - Publisher B</title>
      <link>%s/rss/articles/CBMiYY456</link>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <source url="https://www.publisherb.com">Publisher B</source>
    </item>
  </channel>
</rss>`, srvURL, srvURL)
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(feed))
	})

	mux2.HandleFunc("/rss/articles/CBMiXX123", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.publishera.com/actual-article-1", http.StatusFound)
	})

	mux2.HandleFunc("/rss/articles/CBMiYY456", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.publisherb.com/actual-article-2", http.StatusFound)
	})

	srv = httptest.NewServer(mux2)
	defer srv.Close()
	srvURL = srv.URL

	p := NewParserWithClient(srv.Client())

	articles, err := p.ParseFeed(context.Background(), srv.URL+"/rss")
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}

	if len(articles) != 2 {
		t.Fatalf("Expected 2 articles, got %d", len(articles))
	}

	// Verify URLs are resolved (not the Google redirect URLs).
	if articles[0].URL != "https://www.publishera.com/actual-article-1" {
		t.Errorf("articles[0].URL = %q, want resolved URL", articles[0].URL)
	}
	if articles[1].URL != "https://www.publisherb.com/actual-article-2" {
		t.Errorf("articles[1].URL = %q, want resolved URL", articles[1].URL)
	}

	// Verify other fields are still populated.
	if articles[0].Source != "Publisher A" {
		t.Errorf("articles[0].Source = %q, want %q", articles[0].Source, "Publisher A")
	}
}

func TestParseFeed_SkipsOnRedirectFailure(t *testing.T) {
	var srvURL string
	mux := http.NewServeMux()

	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <item>
      <title>Good Article - Publisher A</title>
      <link>%s/rss/articles/good</link>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <source url="https://www.publishera.com">Publisher A</source>
    </item>
    <item>
      <title>Bad Article - Publisher B</title>
      <link>%s/rss/articles/bad</link>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <source url="https://www.publisherb.com">Publisher B</source>
    </item>
  </channel>
</rss>`, srvURL, srvURL)
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(feed))
	})

	mux.HandleFunc("/rss/articles/good", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.publishera.com/real-article", http.StatusFound)
	})

	mux.HandleFunc("/rss/articles/bad", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	p := NewParserWithClient(srv.Client())

	articles, err := p.ParseFeed(context.Background(), srv.URL+"/rss")
	if err != nil {
		t.Fatalf("ParseFeed() error = %v", err)
	}

	// Only the good article should be returned; the bad one is skipped.
	if len(articles) != 1 {
		t.Fatalf("Expected 1 article (bad skipped), got %d", len(articles))
	}
	if articles[0].Title != "Good Article - Publisher A" {
		t.Errorf("Title = %q, want %q", articles[0].Title, "Good Article - Publisher A")
	}
}

func TestParseFeed_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Test</title></channel></rss>`))
	}))
	defer srv.Close()

	p := NewParserWithClient(srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.ParseFeed(ctx, srv.URL+"/rss")
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `cd backend && go test -v -run "TestParseFeed" ./internal/googlenews/`
Expected: Compilation error — `NewParserWithClient` and `ParseFeed` don't exist yet.

**Step 3: Write the implementation**

Add to `backend/internal/googlenews/parser.go`:

```go
// NewParserWithClient creates a Parser with a custom HTTP client.
// This is useful for testing with httptest servers.
func NewParserWithClient(client *http.Client) *Parser {
	fp := gofeed.NewParser()
	fp.UserAgent = "byline/1.0 (RSS Feed Parser)"

	return &Parser{
		feedParser: fp,
		httpClient: client,
	}
}

// ParseFeed fetches a Google News RSS feed from the given URL, extracts
// articles, and resolves Google redirect URLs to actual article URLs.
// Articles whose redirect resolution fails are silently skipped.
func (p *Parser) ParseFeed(ctx context.Context, feedURL string) ([]Article, error) {
	feed, err := p.feedParser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feed from %s: %w", feedURL, err)
	}

	var articles []Article
	for _, item := range feed.Items {
		a := p.parseItem(item)

		resolved, err := p.resolveURL(ctx, a.URL)
		if err != nil {
			// Skip articles whose redirect resolution fails.
			continue
		}
		a.URL = resolved

		articles = append(articles, a)
	}
	return articles, nil
}

// resolveURL follows redirects from a Google News URL to get the final
// article URL. It uses a non-following HTTP client and reads the Location
// header, or follows the full redirect chain to capture the final URL.
func (p *Parser) resolveURL(ctx context.Context, googleURL string) (string, error) {
	// Create a client that does NOT follow redirects so we can capture
	// the final URL from the redirect chain.
	noRedirectClient := *p.httpClient
	var finalURL string
	noRedirectClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		finalURL = req.URL.String()
		// Allow up to 10 redirects.
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, googleURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("resolving URL %s: %w", googleURL, err)
	}
	defer resp.Body.Close()

	// If we captured a final URL from the redirect chain, use it.
	if finalURL != "" {
		return finalURL, nil
	}

	// If there were no redirects and the status is OK, the original URL is fine.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return googleURL, nil
	}

	return "", fmt.Errorf("unexpected status %d for %s", resp.StatusCode, googleURL)
}
```

**Step 4: Run the tests**

Run: `cd backend && go test -v -run "TestParseFeed" ./internal/googlenews/`
Expected: All tests PASS.

**Step 5: Run full test suite**

Run: `cd backend && go test -v ./internal/googlenews/`
Expected: All tests PASS.

**Step 6: Commit**

```bash
cd backend && git add internal/googlenews/parser.go internal/googlenews/parser_test.go
git commit -m "feat(googlenews): add ParseFeed with redirect resolution"
```

---

### Task 3: Database Migration

**Files:**
- Create: `backend/internal/store/migrations/000003_create_news_articles.up.sql`
- Create: `backend/internal/store/migrations/000003_create_news_articles.down.sql`

**Step 1: Create the up migration**

Create `backend/internal/store/migrations/000003_create_news_articles.up.sql`:

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

**Step 2: Create the down migration**

Create `backend/internal/store/migrations/000003_create_news_articles.down.sql`:

```sql
DROP TABLE IF EXISTS news_articles;
```

**Step 3: Verify migration applies (if DB available)**

Run: `mise run db:migrate:up`
Expected: Migration 3 applied. If no DB, skip this — the integration tests will apply it.

**Step 4: Commit**

```bash
cd backend && git add internal/store/migrations/000003_create_news_articles.up.sql internal/store/migrations/000003_create_news_articles.down.sql
git commit -m "feat(store): add migration for news_articles table"
```

---

### Task 4: Store Interface + LogStore — Add news article methods

**Files:**
- Modify: `backend/internal/store/store.go` (add 5 new methods + `NewsArticleRecord` type)
- Modify: `backend/internal/poller/store.go` (LogStore: implement new methods)

**Step 1: Add methods to the Store interface**

In `backend/internal/store/store.go`, add the `googlenews` import and new interface methods:

Add to imports:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add `NewsArticleRecord` type after `TrendingRepoRecord`:
```go
// NewsArticleRecord extends googlenews.Article with database metadata.
type NewsArticleRecord struct {
	ID int64
	googlenews.Article
	CreatedAt time.Time
	UpdatedAt time.Time
}
```

Add to the `Store` interface (after the trending repos section):
```go
// --- News Articles ---

// UpsertNewsArticle inserts a news article or updates it if the
// article_url already exists.
UpsertNewsArticle(ctx context.Context, article googlenews.Article) (int64, error)

// UpsertNewsArticles batch-upserts multiple news articles in a single transaction.
// Returns the number of rows affected.
UpsertNewsArticles(ctx context.Context, articles []googlenews.Article) (int64, error)

// GetNewsArticleByID retrieves a single news article by its database ID.
GetNewsArticleByID(ctx context.Context, id int64) (*googlenews.Article, error)

// ListNewsArticles retrieves news articles, ordered by published_at desc.
// Supports cursor-based pagination via the opts parameter.
ListNewsArticles(ctx context.Context, opts ListOptions) ([]googlenews.Article, error)

// DeleteNewsArticle removes a news article by its database ID.
// Returns true if a row was deleted, false if no row matched.
DeleteNewsArticle(ctx context.Context, id int64) (bool, error)
```

**Step 2: Implement in LogStore**

In `backend/internal/poller/store.go`, add the `googlenews` import and implement:

Add to imports:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add implementations:
```go
func (s *LogStore) UpsertNewsArticle(_ context.Context, article googlenews.Article) (int64, error) {
	s.logger.Info("upsert news article", "title", article.Title, "url", article.URL)
	return 1, nil
}

func (s *LogStore) UpsertNewsArticles(_ context.Context, articles []googlenews.Article) (int64, error) {
	for _, a := range articles {
		s.logger.Info("upsert news article", "title", a.Title, "url", a.URL)
	}
	return int64(len(articles)), nil
}

func (s *LogStore) GetNewsArticleByID(_ context.Context, _ int64) (*googlenews.Article, error) {
	return nil, store.ErrNotFound
}

func (s *LogStore) ListNewsArticles(_ context.Context, _ store.ListOptions) ([]googlenews.Article, error) {
	return nil, nil
}

func (s *LogStore) DeleteNewsArticle(_ context.Context, _ int64) (bool, error) {
	return false, nil
}
```

**Step 3: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Compilation succeeds (PostgresStore will fail until Task 5, so just check for syntax issues in the modified files). Actually, `PostgresStore` implements `Store` via compile-time check (`var _ Store = (*PostgresStore)(nil)`), so this will fail. Skip this check — Task 5 will fix it.

**Step 4: Commit**

Do NOT commit yet — this will break `go build` because `PostgresStore` doesn't implement the new methods. Proceed to Task 5 immediately.

---

### Task 5: PostgresStore — Implement news article methods

**Files:**
- Modify: `backend/internal/store/postgres.go` (add SQL + methods)

**Step 1: Add SQL constants and helper functions**

In `backend/internal/store/postgres.go`, add the `googlenews` import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add SQL constants after the existing ones:
```go
upsertNewsArticleSQL = `
	INSERT INTO news_articles (title, article_url, source_name, source_url, published_at)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (article_url) DO UPDATE SET
		title       = EXCLUDED.title,
		source_name = EXCLUDED.source_name,
		source_url  = EXCLUDED.source_url,
		published_at = EXCLUDED.published_at,
		updated_at  = NOW()`
```

Add helper function:
```go
func newsArticleArgs(a googlenews.Article) []any {
	return []any{a.Title, a.URL, a.Source, a.SourceURL, a.PublishedAt}
}
```

**Step 2: Implement CRUD methods**

Add after the Trending Repositories section in `postgres.go`:

```go
// ---------------------------------------------------------------------------
// News Articles
// ---------------------------------------------------------------------------

// UpsertNewsArticle inserts a news article or updates it if the
// article_url already exists. Returns the row ID.
func (s *PostgresStore) UpsertNewsArticle(ctx context.Context, article googlenews.Article) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, upsertNewsArticleSQL+"\n\t\tRETURNING id",
		newsArticleArgs(article)...,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting news article: %w", classifyError(err))
	}
	return id, nil
}

// UpsertNewsArticles batch-upserts multiple news articles in a single transaction.
// Returns the number of rows affected.
func (s *PostgresStore) UpsertNewsArticles(ctx context.Context, articles []googlenews.Article) (int64, error) {
	if len(articles) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, a := range articles {
		batch.Queue(upsertNewsArticleSQL, newsArticleArgs(a)...)
	}

	affected, err := s.execBatch(ctx, batch, len(articles))
	if err != nil {
		return 0, fmt.Errorf("batch upserting news articles: %w", classifyError(err))
	}
	return affected, nil
}

// GetNewsArticleByID retrieves a single news article by its database ID.
func (s *PostgresStore) GetNewsArticleByID(ctx context.Context, id int64) (*googlenews.Article, error) {
	var a googlenews.Article
	err := s.pool.QueryRow(ctx, `
		SELECT title, article_url, source_name, source_url, published_at
		FROM news_articles
		WHERE id = $1
	`, id).Scan(&a.Title, &a.URL, &a.Source, &a.SourceURL, &a.PublishedAt)
	if err != nil {
		return nil, mapNotFound(err, "getting news article by ID")
	}
	return &a, nil
}

// ListNewsArticles retrieves news articles, ordered by published_at desc.
// Supports cursor-based pagination via the opts parameter.
func (s *PostgresStore) ListNewsArticles(ctx context.Context, opts ListOptions) ([]googlenews.Article, error) {
	limit := clampLimit(opts.Limit)

	rows, err := s.pool.Query(ctx, `
		SELECT id, title, article_url, source_name, source_url, published_at
		FROM news_articles
		WHERE ($1::bigint = 0 OR id < $1)
		ORDER BY published_at DESC, id DESC
		LIMIT $2
	`, opts.Cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("listing news articles: %w", err)
	}
	defer rows.Close()

	var articles []googlenews.Article
	for rows.Next() {
		var (
			rowID int64
			a     googlenews.Article
		)
		if err := rows.Scan(&rowID, &a.Title, &a.URL, &a.Source, &a.SourceURL, &a.PublishedAt); err != nil {
			return nil, fmt.Errorf("scanning news article row: %w", err)
		}
		articles = append(articles, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating news article rows: %w", err)
	}
	return articles, nil
}

// DeleteNewsArticle removes a news article by its database ID.
// Returns true if a row was deleted, false if no row matched.
func (s *PostgresStore) DeleteNewsArticle(ctx context.Context, id int64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM news_articles WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("deleting news article: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}
```

**Step 3: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Compilation succeeds.

**Step 4: Run existing tests to check for regressions**

Run: `cd backend && go test ./internal/store/ ./internal/poller/ ./internal/reddit/`
Expected: All tests PASS (store integration tests skip without DB).

**Step 5: Commit Tasks 4 + 5 together**

```bash
cd backend && git add internal/store/store.go internal/store/postgres.go ../backend/internal/poller/store.go
git commit -m "feat(store): add news article CRUD methods to Store interface and PostgresStore"
```

> **Note:** You may need to adjust the `git add` paths. The files are:
> - `backend/internal/store/store.go`
> - `backend/internal/store/postgres.go`
> - `backend/internal/poller/store.go`

---

### Task 6: Store Integration Tests

**Files:**
- Modify: `backend/internal/store/postgres_test.go`

**Step 1: Write integration tests**

Append to `backend/internal/store/postgres_test.go`:

Add `googlenews` import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add helper factory:
```go
func makeNewsArticle(url, title, source string) googlenews.Article {
	return googlenews.Article{
		Title:       title,
		URL:         url,
		Source:      source,
		SourceURL:   "https://www." + strings.ToLower(strings.ReplaceAll(source, " ", "")) + ".com",
		PublishedAt: time.Now().Truncate(time.Microsecond).UTC(),
	}
}
```

Add `"strings"` to imports.

Add tests:
```go
// ---------------------------------------------------------------------------
// News Articles
// ---------------------------------------------------------------------------

func TestUpsertNewsArticle_Insert(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeNewsArticle("https://example.com/news/1", "Breaking News", "The Times")
	id, err := s.UpsertNewsArticle(ctx, a)
	if err != nil {
		t.Fatalf("UpsertNewsArticle: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected id > 0, got %d", id)
	}

	got, err := s.GetNewsArticleByID(ctx, id)
	if err != nil {
		t.Fatalf("GetNewsArticleByID: %v", err)
	}
	if got.Title != a.Title {
		t.Errorf("title: got %q, want %q", got.Title, a.Title)
	}
	if got.URL != a.URL {
		t.Errorf("url: got %q, want %q", got.URL, a.URL)
	}
	if got.Source != a.Source {
		t.Errorf("source: got %q, want %q", got.Source, a.Source)
	}
	if got.SourceURL != a.SourceURL {
		t.Errorf("source_url: got %q, want %q", got.SourceURL, a.SourceURL)
	}
	if !got.PublishedAt.Equal(a.PublishedAt) {
		t.Errorf("published_at: got %v, want %v", got.PublishedAt, a.PublishedAt)
	}
}

func TestUpsertNewsArticle_Update(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeNewsArticle("https://example.com/news/upsert", "Original Title", "Publisher")
	id1, err := s.UpsertNewsArticle(ctx, a)
	if err != nil {
		t.Fatalf("first UpsertNewsArticle: %v", err)
	}

	a.Title = "Updated Title"
	id2, err := s.UpsertNewsArticle(ctx, a)
	if err != nil {
		t.Fatalf("second UpsertNewsArticle: %v", err)
	}
	if id1 != id2 {
		t.Errorf("expected same id after upsert: got %d and %d", id1, id2)
	}

	got, err := s.GetNewsArticleByID(ctx, id1)
	if err != nil {
		t.Fatalf("GetNewsArticleByID: %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("title not updated: got %q, want %q", got.Title, "Updated Title")
	}
}

func TestUpsertNewsArticles_Batch(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	articles := make([]googlenews.Article, 10)
	for i := range articles {
		articles[i] = makeNewsArticle(
			fmt.Sprintf("https://example.com/news/batch-%d", i),
			fmt.Sprintf("Batch Article %d", i),
			"Publisher",
		)
	}

	count, err := s.UpsertNewsArticles(ctx, articles)
	if err != nil {
		t.Fatalf("UpsertNewsArticles: %v", err)
	}
	if count != 10 {
		t.Errorf("expected 10 rows affected, got %d", count)
	}

	listed, err := s.ListNewsArticles(ctx, ListOptions{Limit: 20})
	if err != nil {
		t.Fatalf("ListNewsArticles: %v", err)
	}
	if len(listed) != 10 {
		t.Errorf("expected 10 articles listed, got %d", len(listed))
	}
}

func TestUpsertNewsArticles_EmptySlice(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	count, err := s.UpsertNewsArticles(ctx, nil)
	if err != nil {
		t.Fatalf("UpsertNewsArticles(nil): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows affected, got %d", count)
	}
}

func TestGetNewsArticleByID_NotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	_, err := s.GetNewsArticleByID(ctx, 999999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListNewsArticles_Pagination(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 12 {
		a := makeNewsArticle(
			fmt.Sprintf("https://example.com/news/page-%d", i),
			fmt.Sprintf("Page Article %d", i),
			"Publisher",
		)
		a.PublishedAt = base.Add(time.Duration(i) * time.Hour)
		if _, err := s.UpsertNewsArticle(ctx, a); err != nil {
			t.Fatalf("UpsertNewsArticle[%d]: %v", i, err)
		}
	}

	// Page 1.
	page1, err := s.ListNewsArticles(ctx, ListOptions{Limit: 5, Cursor: 0})
	if err != nil {
		t.Fatalf("ListNewsArticles page1: %v", err)
	}
	if len(page1) != 5 {
		t.Fatalf("page1: expected 5, got %d", len(page1))
	}

	// Verify descending order.
	for i := 1; i < len(page1); i++ {
		if page1[i].PublishedAt.After(page1[i-1].PublishedAt) {
			t.Errorf("page1 not in descending order at index %d", i)
		}
	}
}

func TestDeleteNewsArticle(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeNewsArticle("https://example.com/news/delete-me", "Delete Me", "Publisher")
	id, err := s.UpsertNewsArticle(ctx, a)
	if err != nil {
		t.Fatalf("UpsertNewsArticle: %v", err)
	}

	deleted, err := s.DeleteNewsArticle(ctx, id)
	if err != nil {
		t.Fatalf("DeleteNewsArticle: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}

	_, err = s.GetNewsArticleByID(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}
```

**Step 2: Update setupTestStore truncation**

In `setupTestStore`, update the TRUNCATE statements to include `news_articles`:

Change:
```go
_, err = s.pool.Exec(ctx, "TRUNCATE reddit_articles, trending_repos RESTART IDENTITY CASCADE")
```
To:
```go
_, err = s.pool.Exec(ctx, "TRUNCATE reddit_articles, trending_repos, news_articles RESTART IDENTITY CASCADE")
```

And the same in the `t.Cleanup` block.

**Step 3: Run integration tests (if DB available)**

Run: `BYLINE_TEST_DATABASE_URL="..." cd backend && go test -v -run "TestUpsertNewsArticle|TestListNewsArticles|TestDeleteNewsArticle|TestGetNewsArticleByID" ./internal/store/`
Expected: All PASS. If no DB, tests skip cleanly.

**Step 4: Run all store tests to check for regressions**

Run: `cd backend && go test ./internal/store/`
Expected: All PASS (skips integration tests without DB).

**Step 5: Commit**

```bash
cd backend && git add internal/store/postgres_test.go
git commit -m "test(store): add integration tests for news article CRUD"
```

---

### Task 7: Poller Integration — FeedItem conversion + Source adapter + Config

**Files:**
- Modify: `backend/internal/poller/item.go` (add `SourceGoogleNews`, `newsToFeedItem`, `feedItemToNewsArticle`)
- Modify: `backend/internal/poller/source.go` (add `GoogleNewsSource`)
- Modify: `backend/internal/poller/config.go` (add `GoogleNewsConfig`)
- Modify: `backend/internal/poller/poller.go` (add persist routing + config handling)

**Step 1: Add FeedItem conversion functions**

In `backend/internal/poller/item.go`:

Add import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add constant:
```go
// SourceGoogleNews indicates the item was fetched from a Google News RSS feed.
SourceGoogleNews SourceType = "googlenews"
```

Add conversion functions:
```go
// newsToFeedItem converts a googlenews.Article into a normalized FeedItem.
func newsToFeedItem(a googlenews.Article) FeedItem {
	return FeedItem{
		Source:      SourceGoogleNews,
		Title:       a.Title,
		URL:         a.URL,
		Tags:        []string{a.Source},
		PublishedAt: a.PublishedAt,
		FetchedAt:   time.Now(),
		Metadata: map[string]string{
			"source_name": a.Source,
			"source_url":  a.SourceURL,
		},
	}
}

// feedItemToNewsArticle converts a FeedItem back to a googlenews.Article for persistence.
func feedItemToNewsArticle(item FeedItem) googlenews.Article {
	return googlenews.Article{
		Title:       item.Title,
		URL:         item.URL,
		Source:      item.Metadata["source_name"],
		SourceURL:   item.Metadata["source_url"],
		PublishedAt: item.PublishedAt,
	}
}
```

**Step 2: Add GoogleNewsSource adapter**

In `backend/internal/poller/source.go`:

Add import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add compile-time check:
```go
_ Source = (*GoogleNewsSource)(nil)
```

Add source adapter:
```go
// ---------------------------------------------------------------------------
// GoogleNewsSource
// ---------------------------------------------------------------------------

// GoogleNewsSource adapts a googlenews.Parser into the Source interface.
// It fetches a Google News RSS feed and converts articles to FeedItems.
type GoogleNewsSource struct {
	parser  *googlenews.Parser
	feedURL string
}

// NewGoogleNewsSource creates a GoogleNewsSource that fetches the given
// feed URL using the provided parser.
func NewGoogleNewsSource(parser *googlenews.Parser, feedURL string) *GoogleNewsSource {
	return &GoogleNewsSource{parser: parser, feedURL: feedURL}
}

// Name returns "googlenews".
func (s *GoogleNewsSource) Name() string { return "googlenews" }

// Fetch retrieves articles from the Google News RSS feed and converts
// them to FeedItems.
func (s *GoogleNewsSource) Fetch(ctx context.Context) ([]FeedItem, error) {
	articles, err := s.parser.ParseFeed(ctx, s.feedURL)
	if err != nil {
		return nil, err
	}
	items := make([]FeedItem, 0, len(articles))
	for _, a := range articles {
		items = append(items, newsToFeedItem(a))
	}
	return items, nil
}
```

**Step 3: Add GoogleNewsConfig**

In `backend/internal/poller/config.go`, add:

```go
// GoogleNewsConfig holds settings for the Google News RSS feed source.
type GoogleNewsConfig struct {
	// FeedURL is the Google News RSS feed URL to poll.
	FeedURL string

	// Interval is the time between successive polls of the feed.
	Interval time.Duration
}
```

And add the field to `Config`:
```go
// GoogleNews configures the Google News RSS source. If nil, Google News
// polling is disabled.
GoogleNews *GoogleNewsConfig
```

**Step 4: Add persist routing + config handling in poller.go**

In `backend/internal/poller/poller.go`:

Add import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add to `New()` function (after the OSSInsight block):
```go
if cfg.GoogleNews != nil {
	interval := cfg.GoogleNews.Interval
	if interval == 0 {
		interval = 30 * time.Minute
	}
	feedURL := cfg.GoogleNews.FeedURL
	if feedURL == "" {
		feedURL = "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en"
	}
	src := NewGoogleNewsSource(googlenews.NewParser(), feedURL)
	p.runners = append(p.runners, sourceRunner{source: src, interval: interval})
}
```

Add persist case (before the `default` case):
```go
case "googlenews":
	articles := make([]googlenews.Article, 0, len(items))
	for _, item := range items {
		articles = append(articles, feedItemToNewsArticle(item))
	}
	n, err := p.store.UpsertNewsArticles(ctx, articles)
	if err != nil {
		return fmt.Errorf("upserting news articles: %w", err)
	}
	p.logger.Debug("news articles upsert complete", "rows_affected", n)
	return nil
```

**Step 5: Verify compilation**

Run: `cd backend && go build ./...`
Expected: Compilation succeeds.

**Step 6: Commit**

```bash
cd backend && git add internal/poller/item.go internal/poller/source.go internal/poller/config.go internal/poller/poller.go
git commit -m "feat(poller): add GoogleNewsSource adapter, FeedItem conversion, and config"
```

---

### Task 8: Poller Tests — FeedItem conversion, source, and persist routing

**Files:**
- Modify: `backend/internal/poller/item_test.go`
- Modify: `backend/internal/poller/source_test.go`
- Modify: `backend/internal/poller/poller_test.go`

**Step 1: Add FeedItem conversion tests**

Append to `backend/internal/poller/item_test.go`:

Add import:
```go
"github.com/myamout/byline/backend/internal/googlenews"
```

Add tests:
```go
func TestNewsToFeedItem(t *testing.T) {
	published := time.Date(2026, 2, 19, 10, 30, 0, 0, time.UTC)

	article := googlenews.Article{
		Title:       "Breaking News - The Times",
		URL:         "https://www.thetimes.com/article/123",
		Source:      "The Times",
		SourceURL:   "https://www.thetimes.com",
		PublishedAt: published,
	}

	item := newsToFeedItem(article)

	if item.Source != SourceGoogleNews {
		t.Errorf("Source = %q, want %q", item.Source, SourceGoogleNews)
	}
	if item.Title != article.Title {
		t.Errorf("Title = %q, want %q", item.Title, article.Title)
	}
	if item.URL != article.URL {
		t.Errorf("URL = %q, want %q", item.URL, article.URL)
	}
	if item.Author != "" {
		t.Errorf("Author = %q, want empty", item.Author)
	}
	if !item.PublishedAt.Equal(published) {
		t.Errorf("PublishedAt = %v, want %v", item.PublishedAt, published)
	}
	if len(item.Tags) != 1 || item.Tags[0] != "The Times" {
		t.Errorf("Tags = %v, want [The Times]", item.Tags)
	}
	if item.Metadata["source_name"] != "The Times" {
		t.Errorf("Metadata[source_name] = %q, want %q", item.Metadata["source_name"], "The Times")
	}
	if item.Metadata["source_url"] != "https://www.thetimes.com" {
		t.Errorf("Metadata[source_url] = %q, want %q", item.Metadata["source_url"], "https://www.thetimes.com")
	}
}

func TestNewsToFeedItem_FetchedAtIsSet(t *testing.T) {
	before := time.Now()
	article := googlenews.Article{Title: "Test", URL: "https://example.com"}
	item := newsToFeedItem(article)
	after := time.Now()

	if item.FetchedAt.Before(before) || item.FetchedAt.After(after) {
		t.Errorf("FetchedAt = %v, want between %v and %v", item.FetchedAt, before, after)
	}
}

func TestFeedItemToNewsArticle(t *testing.T) {
	published := time.Date(2026, 2, 19, 10, 30, 0, 0, time.UTC)

	item := FeedItem{
		Source:      SourceGoogleNews,
		Title:       "Breaking News - The Times",
		URL:         "https://www.thetimes.com/article/123",
		PublishedAt: published,
		Metadata: map[string]string{
			"source_name": "The Times",
			"source_url":  "https://www.thetimes.com",
		},
	}

	article := feedItemToNewsArticle(item)

	if article.Title != "Breaking News - The Times" {
		t.Errorf("Title = %q, want %q", article.Title, "Breaking News - The Times")
	}
	if article.URL != "https://www.thetimes.com/article/123" {
		t.Errorf("URL = %q, want %q", article.URL, "https://www.thetimes.com/article/123")
	}
	if article.Source != "The Times" {
		t.Errorf("Source = %q, want %q", article.Source, "The Times")
	}
	if article.SourceURL != "https://www.thetimes.com" {
		t.Errorf("SourceURL = %q, want %q", article.SourceURL, "https://www.thetimes.com")
	}
	if !article.PublishedAt.Equal(published) {
		t.Errorf("PublishedAt = %v, want %v", article.PublishedAt, published)
	}
}

func TestFeedItemToNewsArticle_MissingMetadata(t *testing.T) {
	item := FeedItem{
		Source:   SourceGoogleNews,
		Title:    "Article",
		URL:      "https://example.com",
		Metadata: map[string]string{},
	}

	article := feedItemToNewsArticle(item)

	if article.Source != "" {
		t.Errorf("Source = %q, want empty", article.Source)
	}
	if article.SourceURL != "" {
		t.Errorf("SourceURL = %q, want empty", article.SourceURL)
	}
}
```

**Step 2: Add GoogleNewsSource tests**

Append to `backend/internal/poller/source_test.go`:

Add test feed data constant:
```go
const testGoogleNewsFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <item>
      <title>Test News Article 1 - Publisher A</title>
      <link>FEEDURL/redirect/1</link>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <source url="https://www.publishera.com">Publisher A</source>
    </item>
    <item>
      <title>Test News Article 2 - Publisher B</title>
      <link>FEEDURL/redirect/2</link>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <source url="https://www.publisherb.com">Publisher B</source>
    </item>
  </channel>
</rss>`
```

Add import: `"strings"`, `"github.com/myamout/byline/backend/internal/googlenews"`

Add tests:
```go
func TestGoogleNewsSource_Name(t *testing.T) {
	src := NewGoogleNewsSource(googlenews.NewParser(), "https://example.com/rss")
	if got := src.Name(); got != "googlenews" {
		t.Errorf("Name() = %q, want %q", got, "googlenews")
	}
}

func TestGoogleNewsSource_Fetch_HTTPTest(t *testing.T) {
	var srvURL string
	mux := http.NewServeMux()

	mux.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := strings.ReplaceAll(testGoogleNewsFeed, "FEEDURL", srvURL)
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(feed))
	})

	// Redirect endpoints resolve to real URLs.
	mux.HandleFunc("/redirect/1", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.publishera.com/article/1", http.StatusFound)
	})
	mux.HandleFunc("/redirect/2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://www.publisherb.com/article/2", http.StatusFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	parser := googlenews.NewParserWithClient(srv.Client())
	src := NewGoogleNewsSource(parser, srv.URL+"/rss")

	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Fetch() returned %d items, want 2", len(items))
	}

	first := items[0]
	if first.Source != SourceGoogleNews {
		t.Errorf("items[0].Source = %q, want %q", first.Source, SourceGoogleNews)
	}
	if first.Title != "Test News Article 1 - Publisher A" {
		t.Errorf("items[0].Title = %q, want %q", first.Title, "Test News Article 1 - Publisher A")
	}
	if first.URL != "https://www.publishera.com/article/1" {
		t.Errorf("items[0].URL = %q, want resolved URL", first.URL)
	}
	if len(first.Tags) != 1 || first.Tags[0] != "Publisher A" {
		t.Errorf("items[0].Tags = %v, want [Publisher A]", first.Tags)
	}
}

func TestGoogleNewsSource_Fetch_CancelledContext(t *testing.T) {
	parser := googlenews.NewParser()
	src := NewGoogleNewsSource(parser, "https://news.google.com/rss")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := src.Fetch(ctx)
	if err == nil {
		t.Error("Expected error for cancelled context, got nil")
	}
}
```

**Step 3: Add persist routing test + update recordingStore**

In `backend/internal/poller/poller_test.go`:

Add import: `"github.com/myamout/byline/backend/internal/googlenews"`

Add to `recordingStore`:
```go
upsertNewsCalls    int
upsertNewsArticles []googlenews.Article
```

Add method:
```go
func (s *recordingStore) UpsertNewsArticles(ctx context.Context, articles []googlenews.Article) (int64, error) {
	s.mu.Lock()
	s.upsertNewsCalls++
	s.upsertNewsArticles = append(s.upsertNewsArticles, articles...)
	s.mu.Unlock()
	return s.LogStore.UpsertNewsArticles(ctx, articles)
}
```

Add helper:
```go
func googleNewsFeedItems(n int) []FeedItem {
	items := make([]FeedItem, n)
	for i := range items {
		items[i] = FeedItem{
			Source:      SourceGoogleNews,
			Title:       "news article",
			URL:         "https://example.com/news/article",
			PublishedAt: time.Now(),
			Metadata: map[string]string{
				"source_name": "Publisher",
				"source_url":  "https://www.publisher.com",
			},
		}
	}
	return items
}
```

Add test:
```go
func TestPoller_Persist_GoogleNewsRouting(t *testing.T) {
	logger := newTestLogger()
	rec := &recordingStore{LogStore: NewLogStore(logger)}

	src := &fakeSource{
		name:  "googlenews",
		items: googleNewsFeedItems(3),
	}

	p := NewBase(rec, logger, 5*time.Second)
	p.AddSource(src, 1*time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.upsertNewsCalls == 0 {
		t.Fatal("UpsertNewsArticles was never called")
	}
	if rec.upsertCalls != 0 {
		t.Errorf("UpsertArticles was called %d times, want 0 for googlenews source", rec.upsertCalls)
	}
	if rec.insertCalls != 0 {
		t.Errorf("InsertTrendingRepos was called %d times, want 0 for googlenews source", rec.insertCalls)
	}
	if len(rec.upsertNewsArticles) != 3 {
		t.Errorf("UpsertNewsArticles received %d articles, want 3", len(rec.upsertNewsArticles))
	}
}
```

**Step 4: Run all poller tests**

Run: `cd backend && go test -v ./internal/poller/`
Expected: All tests PASS.

**Step 5: Run full test suite**

Run: `cd backend && go test ./...`
Expected: All tests PASS (store integration tests skip without DB).

**Step 6: Commit**

```bash
cd backend && git add internal/poller/item_test.go internal/poller/source_test.go internal/poller/poller_test.go
git commit -m "test(poller): add Google News FeedItem conversion, source, and routing tests"
```

---

### Task 9: Update poller main entry point

**Files:**
- Modify: `backend/cmd/poller/main.go`

**Step 1: Add Google News config to main**

In `backend/cmd/poller/main.go`, add to the config:

```go
GoogleNews: &poller.GoogleNewsConfig{
	FeedURL:  "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en",
	Interval: 30 * time.Minute,
},
```

Update the logger line to include Google News:
```go
logger.Info("poller starting",
	"subreddits", cfg.Reddit.Subreddits,
	"reddit_interval", cfg.Reddit.Interval,
	"ossinsight_interval", cfg.OSSInsight.Interval,
	"googlenews_feed", cfg.GoogleNews.FeedURL,
	"googlenews_interval", cfg.GoogleNews.Interval,
)
```

**Step 2: Verify compilation**

Run: `cd backend && go build ./cmd/poller/`
Expected: Compilation succeeds.

**Step 3: Commit**

```bash
cd backend && git add cmd/poller/main.go
git commit -m "feat(poller): enable Google News source in poller main"
```

---

### Task 10: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Update the architecture docs**

Update the package tree to include `googlenews/`:
```
backend/
  cmd/
    poller/main.go
    migrate/main.go
  internal/
    reddit/
    ossinsight/
    googlenews/          # Google News RSS parser → Article structs
    poller/
    store/
```

Update the data flow diagram to include Google News:
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
  "reddit"     → feedItemToArticle()       → store.UpsertArticles()      (ON CONFLICT upsert)
  "ossinsight" → feedItemToRepository()    → store.InsertTrendingRepos() (point-in-time snapshot)
  "googlenews" → feedItemToNewsArticle()   → store.UpsertNewsArticles()  (ON CONFLICT upsert)
```

Add `googlenews` to the Key packages section:
```
**`googlenews`** — `Parser` wraps `gofeed.Parser` + `http.Client`. Entry points: `ParseFeed` (fetches + resolves redirects), `ParseString` (for testing). Resolves Google News redirect URLs to actual article URLs via HTTP HEAD. Output: `Article` struct.
```

Update the persist routing section and concurrency model as appropriate.

**Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md with Google News source"
```

---

### Task 11: Final verification

**Step 1: Run full test suite**

Run: `cd backend && go test -race ./...`
Expected: All tests PASS with race detector.

**Step 2: Verify clean build**

Run: `cd backend && go build ./...`
Expected: Clean build.

**Step 3: Verify no uncommitted changes**

Run: `git status`
Expected: Clean working tree.
