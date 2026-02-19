package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/myamout/byline/backend/internal/googlenews"
	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

// ---------------------------------------------------------------------------
// Test feed data
// ---------------------------------------------------------------------------

// testRedditFeed is a minimal Atom feed with two external articles and one
// self-post (which the parser should filter out).
const testRedditFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>r/golang</title>
  <entry>
    <author><name>/u/testuser</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;a href="https://example.com/article1"&gt;[link]&lt;/a&gt;</content>
    <link href="https://reddit.com/r/golang/comments/abc123"/>
    <title>Test Article 1</title>
    <updated>2024-01-15T10:00:00Z</updated>
  </entry>
  <entry>
    <author><name>/u/testuser2</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;a href="https://example.com/article2"&gt;[link]&lt;/a&gt;</content>
    <link href="https://reddit.com/r/golang/comments/def456"/>
    <title>Test Article 2</title>
    <updated>2024-01-15T11:00:00Z</updated>
  </entry>
  <entry>
    <author><name>/u/selfposter</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;a href="https://www.reddit.com/r/golang/self"&gt;[link]&lt;/a&gt;</content>
    <link href="https://reddit.com/r/golang/comments/ghi789"/>
    <title>Self Post</title>
    <updated>2024-01-15T12:00:00Z</updated>
  </entry>
</feed>`

// testOSSInsightResponse is a valid JSON response matching the OSSInsight API
// format, containing two trending repositories.
const testOSSInsightResponse = `{
  "type": "sql_endpoint",
  "data": {
    "columns": [
      {"col": "repo_id", "data_type": "INT", "nullable": true}
    ],
    "rows": [
      {
        "repo_id": 1001,
        "repo_name": "octocat/hello-world",
        "primary_language": "Go",
        "description": "A test repository",
        "stars": 5000,
        "forks": 1200,
        "pull_requests": 80,
        "pushes": 400,
        "total_score": 6680,
        "contributor_logins": "alice,bob",
        "collection_names": "awesome-go"
      },
      {
        "repo_id": 1002,
        "repo_name": "org/project",
        "primary_language": "Rust",
        "description": "Another test repo",
        "stars": 3000,
        "forks": 600,
        "pull_requests": 40,
        "pushes": 200,
        "total_score": 3840,
        "contributor_logins": "charlie",
        "collection_names": ""
      }
    ],
    "result": {
      "code": 200,
      "message": "Query OK!",
      "request_id": "req-abc-123",
      "started_at": 1700000000,
      "finished_at": 1700000001,
      "latency": "1s",
      "row_count": 2,
      "limit": 30,
      "databases": ["gharchive_dev"]
    }
  }
}`

// ---------------------------------------------------------------------------
// RedditSource tests
// ---------------------------------------------------------------------------

func TestRedditSource_Name(t *testing.T) {
	src := NewRedditSource(reddit.NewParser(), []string{"golang"})
	if got := src.Name(); got != "reddit" {
		t.Errorf("Name() = %q, want %q", got, "reddit")
	}
}

func TestNewRedditSource(t *testing.T) {
	parser := reddit.NewParser()
	subs := []string{"golang", "rust", "programming"}

	src := NewRedditSource(parser, subs)

	if src.parser != parser {
		t.Error("parser field not set correctly")
	}
	if !slices.Equal(src.subreddits, subs) {
		t.Errorf("subreddits = %v, want %v", src.subreddits, subs)
	}
}

func TestRedditSource_Fetch_CancelledContext(t *testing.T) {
	parser := reddit.NewParser()
	src := NewRedditSource(parser, []string{"golang", "rust"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Fetch should log errors for each subreddit but still return (nil, nil).
	// Because ParseSubreddit makes an HTTP call that will fail with a cancelled
	// context, each subreddit triggers the error branch and is skipped.
	items, err := src.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}

	// With a cancelled context every subreddit fails, so we expect zero items.
	if len(items) != 0 {
		t.Errorf("Fetch() returned %d items, want 0", len(items))
	}
}

func TestRedditSource_Fetch_EmptySubreddits(t *testing.T) {
	parser := reddit.NewParser()
	src := NewRedditSource(parser, nil)

	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("Fetch() returned %d items, want 0 for nil subreddits", len(items))
	}

	src2 := NewRedditSource(parser, []string{})
	items2, err := src2.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}
	if len(items2) != 0 {
		t.Errorf("Fetch() returned %d items, want 0 for empty subreddits", len(items2))
	}
}

func TestRedditSource_Fetch_HTTPTest(t *testing.T) {
	// Stand up a test server that serves the Atom feed for any request path
	// matching /r/<subreddit>/.rss.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		w.Write([]byte(testRedditFeed))
	}))
	defer srv.Close()

	// reddit.Parser.ParseSubreddit hardcodes the Reddit URL, so we cannot
	// redirect it to our test server directly. Instead, exercise the full
	// adapter code path by using ParseURL (which the adapter delegates to
	// via ParseSubreddit) through a thin wrapper that replaces the URL.
	//
	// To test the adapter itself end-to-end we create a small helper source
	// that uses ParseURL with our test server URL.
	parser := reddit.NewParser()

	// Manually call ParseURL pointed at our test server to verify the data
	// flow from HTTP -> parser -> toFeedItem is correct.
	articles, err := parser.ParseURL(context.Background(), srv.URL+"/r/golang/.rss")
	if err != nil {
		t.Fatalf("ParseURL() error: %v", err)
	}

	// The test feed has 3 entries: 2 external articles + 1 self-post.
	// The parser should filter out the self-post.
	if len(articles) != 2 {
		t.Fatalf("ParseURL() returned %d articles, want 2", len(articles))
	}

	// Convert through the same path Fetch uses and verify FeedItem fields.
	var items []FeedItem
	for _, a := range articles {
		items = append(items, toFeedItem(a))
	}

	// Verify first item.
	first := items[0]
	if first.Source != SourceReddit {
		t.Errorf("items[0].Source = %q, want %q", first.Source, SourceReddit)
	}
	if first.Title != "Test Article 1" {
		t.Errorf("items[0].Title = %q, want %q", first.Title, "Test Article 1")
	}
	if first.URL != "https://example.com/article1" {
		t.Errorf("items[0].URL = %q, want %q", first.URL, "https://example.com/article1")
	}
	if first.Author != "testuser" {
		t.Errorf("items[0].Author = %q, want %q", first.Author, "testuser")
	}
	if len(first.Tags) != 1 || first.Tags[0] != "golang" {
		t.Errorf("items[0].Tags = %v, want [golang]", first.Tags)
	}

	// Verify second item.
	second := items[1]
	if second.Title != "Test Article 2" {
		t.Errorf("items[1].Title = %q, want %q", second.Title, "Test Article 2")
	}
	if second.URL != "https://example.com/article2" {
		t.Errorf("items[1].URL = %q, want %q", second.URL, "https://example.com/article2")
	}
	if second.Author != "testuser2" {
		t.Errorf("items[1].Author = %q, want %q", second.Author, "testuser2")
	}
}

// ---------------------------------------------------------------------------
// OSSInsightSource tests
// ---------------------------------------------------------------------------

func TestOSSInsightSource_Name(t *testing.T) {
	src := NewOSSInsightSource(ossinsight.NewClient(), nil)
	if got := src.Name(); got != "ossinsight" {
		t.Errorf("Name() = %q, want %q", got, "ossinsight")
	}
}

func TestNewOSSInsightSource(t *testing.T) {
	client := ossinsight.NewClient()
	queries := []ossinsight.TrendingReposOptions{
		{Period: ossinsight.PeriodPastWeek, Language: "Go"},
		{Period: ossinsight.PeriodPastMonth, Language: "Rust"},
	}

	src := NewOSSInsightSource(client, queries)

	if src.client != client {
		t.Error("client field not set correctly")
	}
	if !slices.Equal(src.queries, queries) {
		t.Errorf("queries = %+v, want %+v", src.queries, queries)
	}
}

func TestOSSInsightSource_Fetch_CancelledContext(t *testing.T) {
	client := ossinsight.NewClient()
	queries := []ossinsight.TrendingReposOptions{
		{Period: ossinsight.PeriodPast24Hours, Language: "Go"},
	}
	src := NewOSSInsightSource(client, queries)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	items, err := src.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("Fetch() returned %d items, want 0", len(items))
	}
}

func TestOSSInsightSource_Fetch_EmptyQueries(t *testing.T) {
	client := ossinsight.NewClient()

	src := NewOSSInsightSource(client, nil)
	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("Fetch() returned %d items, want 0 for nil queries", len(items))
	}

	src2 := NewOSSInsightSource(client, []ossinsight.TrendingReposOptions{})
	items2, err := src2.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}
	if len(items2) != 0 {
		t.Errorf("Fetch() returned %d items, want 0 for empty queries", len(items2))
	}
}

func TestOSSInsightSource_Fetch_HTTPTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/trending/repos" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testOSSInsightResponse))
	}))
	defer srv.Close()

	client := ossinsight.NewClientWithBaseURL(srv.URL, srv.Client())
	queries := []ossinsight.TrendingReposOptions{
		{Period: ossinsight.PeriodPastWeek, Language: "Go"},
	}
	src := NewOSSInsightSource(client, queries)

	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("Fetch() returned %d items, want 2", len(items))
	}

	// Verify first item.
	first := items[0]
	if first.Source != SourceOSSInsight {
		t.Errorf("items[0].Source = %q, want %q", first.Source, SourceOSSInsight)
	}
	if first.Title != "octocat/hello-world" {
		t.Errorf("items[0].Title = %q, want %q", first.Title, "octocat/hello-world")
	}
	if want := "https://github.com/octocat/hello-world"; first.URL != want {
		t.Errorf("items[0].URL = %q, want %q", first.URL, want)
	}
	if first.Description != "A test repository" {
		t.Errorf("items[0].Description = %q, want %q", first.Description, "A test repository")
	}
	if len(first.Tags) != 1 || first.Tags[0] != "Go" {
		t.Errorf("items[0].Tags = %v, want [Go]", first.Tags)
	}
	if first.Metadata["stars"] != "5000" {
		t.Errorf("items[0].Metadata[stars] = %q, want %q", first.Metadata["stars"], "5000")
	}
	if first.Metadata["total_score"] != "6680" {
		t.Errorf("items[0].Metadata[total_score] = %q, want %q", first.Metadata["total_score"], "6680")
	}

	// Verify second item.
	second := items[1]
	if second.Title != "org/project" {
		t.Errorf("items[1].Title = %q, want %q", second.Title, "org/project")
	}
	if second.Description != "Another test repo" {
		t.Errorf("items[1].Description = %q, want %q", second.Description, "Another test repo")
	}
	if len(second.Tags) != 1 || second.Tags[0] != "Rust" {
		t.Errorf("items[1].Tags = %v, want [Rust]", second.Tags)
	}
}

func TestOSSInsightSource_Fetch_MultipleQueries(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testOSSInsightResponse))
	}))
	defer srv.Close()

	client := ossinsight.NewClientWithBaseURL(srv.URL, srv.Client())
	queries := []ossinsight.TrendingReposOptions{
		{Period: ossinsight.PeriodPast24Hours, Language: "Go"},
		{Period: ossinsight.PeriodPastWeek, Language: "Rust"},
		{Period: ossinsight.PeriodPastMonth, Language: "Python"},
	}
	src := NewOSSInsightSource(client, queries)

	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}

	// Each query returns 2 repos; 3 queries should yield 6 items.
	if len(items) != 6 {
		t.Errorf("Fetch() returned %d items, want 6", len(items))
	}

	if callCount != 3 {
		t.Errorf("server received %d requests, want 3", callCount)
	}
}

func TestOSSInsightSource_Fetch_PartialFailure(t *testing.T) {
	requestNum := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNum++
		if requestNum == 2 {
			// Fail the second query.
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "internal error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(testOSSInsightResponse))
	}))
	defer srv.Close()

	client := ossinsight.NewClientWithBaseURL(srv.URL, srv.Client())
	queries := []ossinsight.TrendingReposOptions{
		{Period: ossinsight.PeriodPast24Hours, Language: "Go"},
		{Period: ossinsight.PeriodPastWeek, Language: "Rust"},   // This one will fail.
		{Period: ossinsight.PeriodPastMonth, Language: "Python"}, // This one should still succeed.
	}
	src := NewOSSInsightSource(client, queries)

	items, err := src.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() returned unexpected error: %v", err)
	}

	// Queries 1 and 3 succeed (2 repos each), query 2 fails: expect 4 items.
	if len(items) != 4 {
		t.Errorf("Fetch() returned %d items, want 4 (partial failure)", len(items))
	}
}

func TestOSSInsightSource_Fetch_QueryParams(t *testing.T) {
	tests := []struct {
		name         string
		query        ossinsight.TrendingReposOptions
		wantPeriod   string
		wantLanguage string
	}{
		{
			name:         "period and language",
			query:        ossinsight.TrendingReposOptions{Period: ossinsight.PeriodPastWeek, Language: "Go"},
			wantPeriod:   "past_week",
			wantLanguage: "Go",
		},
		{
			name:         "period only",
			query:        ossinsight.TrendingReposOptions{Period: ossinsight.PeriodPast24Hours},
			wantPeriod:   "past_24_hours",
			wantLanguage: "",
		},
		{
			name:         "no options",
			query:        ossinsight.TrendingReposOptions{},
			wantPeriod:   "",
			wantLanguage: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPeriod := r.URL.Query().Get("period")
				gotLanguage := r.URL.Query().Get("language")

				if gotPeriod != tc.wantPeriod {
					t.Errorf("period param = %q, want %q", gotPeriod, tc.wantPeriod)
				}
				if gotLanguage != tc.wantLanguage {
					t.Errorf("language param = %q, want %q", gotLanguage, tc.wantLanguage)
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(testOSSInsightResponse))
			}))
			defer srv.Close()

			client := ossinsight.NewClientWithBaseURL(srv.URL, srv.Client())
			src := NewOSSInsightSource(client, []ossinsight.TrendingReposOptions{tc.query})

			_, err := src.Fetch(context.Background())
			if err != nil {
				t.Fatalf("Fetch() returned unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GoogleNewsSource tests
// ---------------------------------------------------------------------------

func TestGoogleNewsSource_Name(t *testing.T) {
	src := NewGoogleNewsSource(googlenews.NewParser(), "https://example.com/rss")
	if got := src.Name(); got != "googlenews" {
		t.Errorf("Name() = %q, want %q", got, "googlenews")
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
