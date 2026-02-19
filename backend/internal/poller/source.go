package poller

import (
	"context"
	"log/slog"

	"github.com/myamout/byline/backend/internal/googlenews"
	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

// Source represents a single data feed that can be polled for new items.
// Implementations are responsible for calling the upstream API and converting
// the results into normalized FeedItem values.
type Source interface {
	// Name returns a human-readable identifier for this source (e.g. "reddit", "ossinsight").
	Name() string

	// Fetch retrieves the latest items from the source. The provided context
	// should be used for cancellation and deadline propagation.
	Fetch(ctx context.Context) ([]FeedItem, error)
}

// Compile-time interface conformance checks.
var (
	_ Source = (*RedditSource)(nil)
	_ Source = (*OSSInsightSource)(nil)
	_ Source = (*GoogleNewsSource)(nil)
)

// ---------------------------------------------------------------------------
// RedditSource
// ---------------------------------------------------------------------------

// RedditSource adapts a reddit.Parser into the Source interface. It polls one
// or more subreddits for external article links and converts them to FeedItems.
type RedditSource struct {
	parser     *reddit.Parser
	subreddits []string
}

// NewRedditSource creates a RedditSource that fetches RSS feeds for the given
// subreddits using the provided parser.
func NewRedditSource(parser *reddit.Parser, subreddits []string) *RedditSource {
	return &RedditSource{parser: parser, subreddits: subreddits}
}

// Name returns "reddit".
func (s *RedditSource) Name() string { return "reddit" }

// Fetch iterates over the configured subreddits, fetches each RSS feed, and
// returns the aggregated list of FeedItems. Individual subreddit failures are
// logged and skipped so that one broken feed does not block the rest.
func (s *RedditSource) Fetch(ctx context.Context) ([]FeedItem, error) {
	var items []FeedItem
	for _, sub := range s.subreddits {
		articles, err := s.parser.ParseSubreddit(ctx, sub)
		if err != nil {
			slog.Error("failed to fetch subreddit", "subreddit", sub, "error", err)
			continue
		}
		for _, a := range articles {
			items = append(items, toFeedItem(a))
		}
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// OSSInsightSource
// ---------------------------------------------------------------------------

// OSSInsightSource adapts an ossinsight.Client into the Source interface. It
// executes one or more trending-repo queries and converts the results to
// FeedItems.
type OSSInsightSource struct {
	client  *ossinsight.Client
	queries []ossinsight.TrendingReposOptions
}

// NewOSSInsightSource creates an OSSInsightSource that executes the given
// queries using the provided client.
func NewOSSInsightSource(client *ossinsight.Client, queries []ossinsight.TrendingReposOptions) *OSSInsightSource {
	return &OSSInsightSource{client: client, queries: queries}
}

// Name returns "ossinsight".
func (s *OSSInsightSource) Name() string { return "ossinsight" }

// Fetch iterates over the configured queries, calls the OSSInsight API for
// each, and returns the aggregated list of FeedItems. Individual query failures
// are logged and skipped so that one broken query does not block the rest.
func (s *OSSInsightSource) Fetch(ctx context.Context) ([]FeedItem, error) {
	var items []FeedItem
	for _, q := range s.queries {
		repos, err := s.client.GetTrendingRepos(ctx, q)
		if err != nil {
			slog.Error("failed to fetch trending repos",
				"period", string(q.Period),
				"language", q.Language,
				"error", err,
			)
			continue
		}
		for _, r := range repos {
			items = append(items, repoToFeedItem(r))
		}
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// GoogleNewsSource
// ---------------------------------------------------------------------------

// GoogleNewsSource adapts a googlenews.Parser into the Source interface.
type GoogleNewsSource struct {
	parser  *googlenews.Parser
	feedURL string
}

// NewGoogleNewsSource creates a GoogleNewsSource that fetches the given feed URL.
func NewGoogleNewsSource(parser *googlenews.Parser, feedURL string) *GoogleNewsSource {
	return &GoogleNewsSource{parser: parser, feedURL: feedURL}
}

// Name returns "googlenews".
func (s *GoogleNewsSource) Name() string { return "googlenews" }

// Fetch retrieves articles from the Google News RSS feed and converts them to FeedItems.
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
