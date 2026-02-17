package poller

import (
	"time"

	"github.com/myamout/byline/backend/internal/ossinsight"
)

// Config holds the top-level configuration for the poller engine.
type Config struct {
	// Reddit configures the Reddit RSS source. If nil, Reddit polling is disabled.
	Reddit *RedditConfig

	// OSSInsight configures the OSSInsight trending repos source. If nil,
	// OSSInsight polling is disabled.
	OSSInsight *OSSInsightConfig

	// FetchTimeout is the maximum duration allowed for a single fetch
	// operation across all sources.
	FetchTimeout time.Duration
}

// RedditConfig holds settings for the Reddit RSS feed source.
type RedditConfig struct {
	// Subreddits is the list of subreddit names to poll (e.g. ["golang", "rust"]).
	Subreddits []string

	// Interval is the time between successive polls of the Reddit feeds.
	Interval time.Duration
}

// OSSInsightConfig holds settings for the OSSInsight trending repos source.
type OSSInsightConfig struct {
	// Queries is the set of trending repo queries to execute on each poll cycle.
	Queries []ossinsight.TrendingReposOptions

	// Interval is the time between successive polls of the OSSInsight API.
	Interval time.Duration
}
