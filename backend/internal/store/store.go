// Package store defines the persistence layer for Byline's data sources.
// It provides a Store interface for database operations and common types
// used across implementations.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

var (
	// ErrNotFound is returned when a requested record does not exist.
	ErrNotFound = errors.New("store: record not found")

	// ErrDuplicateKey is returned when an insert violates a unique constraint
	// and no ON CONFLICT clause was used.
	ErrDuplicateKey = errors.New("store: duplicate key")
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
	ID int64
	ossinsight.Repository
	TrendingPeriod   string
	TrendingLanguage string
	FetchedAt        time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}
