package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

const (
	defaultLimit = 50
	maxLimit     = 200
)

// SQL fragments shared between single and batch operations.
const (
	upsertArticleSQL = `
		INSERT INTO reddit_articles (author, subreddit, posted_at, article_link, title, reddit_link)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (subreddit, article_link) DO UPDATE SET
			author      = EXCLUDED.author,
			title       = EXCLUDED.title,
			reddit_link = EXCLUDED.reddit_link,
			posted_at   = EXCLUDED.posted_at,
			updated_at  = NOW()`

	insertTrendingRepoSQL = `
		INSERT INTO trending_repos (
			repo_id, repo_name, primary_language, description,
			stars, forks, pull_requests, pushes, total_score,
			contributor_logins, collection_names,
			trending_period, trending_language
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`

	trendingRepoColumns = `
		id,
		repo_id, repo_name, primary_language, description,
		stars, forks, pull_requests, pushes, total_score,
		contributor_logins, collection_names,
		trending_period, trending_language,
		fetched_at, created_at, updated_at`
)

// PostgresStore implements Store using pgxpool.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time check that PostgresStore implements Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore creates a new PostgresStore from a standard Postgres connection
// string. The connection string format works with any Postgres provider:
//
//	postgres://user:password@host:5432/dbname?sslmode=require
func NewPostgresStore(ctx context.Context, connString string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parsing connection string: %w", err)
	}

	config.MaxConns = 10
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 5 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

// Ping verifies the database connection is alive.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// Close releases all database resources.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// ---------------------------------------------------------------------------
// Reddit Articles
// ---------------------------------------------------------------------------

// articleArgs returns the query arguments for an article upsert, in column order.
func articleArgs(a reddit.Article) []any {
	return []any{a.Author, a.Subreddit, a.PostedAt, a.ArticleLink, a.Title, a.RedditLink}
}

// UpsertArticle inserts a Reddit article or updates it if the
// (subreddit, article_link) pair already exists. Returns the row ID.
func (s *PostgresStore) UpsertArticle(ctx context.Context, article reddit.Article) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, upsertArticleSQL+"\n\t\tRETURNING id",
		articleArgs(article)...,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upserting article: %w", classifyError(err))
	}
	return id, nil
}

// UpsertArticles batch-upserts multiple articles in a single transaction.
// Returns the number of rows affected.
func (s *PostgresStore) UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error) {
	if len(articles) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, a := range articles {
		batch.Queue(upsertArticleSQL, articleArgs(a)...)
	}

	affected, err := s.execBatch(ctx, batch, len(articles))
	if err != nil {
		return 0, fmt.Errorf("batch upserting articles: %w", classifyError(err))
	}
	return affected, nil
}

// GetArticleByID retrieves a single article by its database ID.
func (s *PostgresStore) GetArticleByID(ctx context.Context, id int64) (*reddit.Article, error) {
	var a reddit.Article
	err := s.pool.QueryRow(ctx, `
		SELECT author, subreddit, posted_at, article_link, title, reddit_link
		FROM reddit_articles
		WHERE id = $1
	`, id).Scan(&a.Author, &a.Subreddit, &a.PostedAt,
		&a.ArticleLink, &a.Title, &a.RedditLink)
	if err != nil {
		return nil, mapNotFound(err, "getting article by ID")
	}
	return &a, nil
}

// ListArticles retrieves articles for a subreddit, ordered by posted_at desc.
// Supports cursor-based pagination via the opts parameter.
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
			rowID int64
			a     reddit.Article
		)
		if err := rows.Scan(&rowID, &a.Author, &a.Subreddit, &a.PostedAt,
			&a.ArticleLink, &a.Title, &a.RedditLink); err != nil {
			return nil, fmt.Errorf("scanning article row: %w", err)
		}
		articles = append(articles, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating article rows: %w", err)
	}
	return articles, nil
}

// DeleteArticle removes an article by its database ID.
// Returns true if a row was deleted, false if no row matched.
func (s *PostgresStore) DeleteArticle(ctx context.Context, id int64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM reddit_articles WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("deleting article: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}

// ---------------------------------------------------------------------------
// Trending Repositories
// ---------------------------------------------------------------------------

// trendingRepoArgs returns the query arguments for a trending repo insert,
// in column order.
func trendingRepoArgs(r ossinsight.Repository, period, language string) []any {
	return []any{
		r.RepoID, r.RepoName, r.PrimaryLanguage, r.Description,
		r.Stars, r.Forks, r.PullRequests, r.Pushes, r.TotalScore,
		r.ContributorLogins, r.CollectionNames,
		period, language,
	}
}

// scanTrendingRepoDest returns scan destinations for a TrendingRepoRecord,
// matching the column order of trendingRepoColumns.
func scanTrendingRepoDest(r *TrendingRepoRecord) []any {
	return []any{
		&r.ID,
		&r.RepoID, &r.RepoName, &r.PrimaryLanguage, &r.Description,
		&r.Stars, &r.Forks, &r.PullRequests, &r.Pushes, &r.TotalScore,
		&r.ContributorLogins, &r.CollectionNames,
		&r.TrendingPeriod, &r.TrendingLanguage,
		&r.FetchedAt, &r.CreatedAt, &r.UpdatedAt,
	}
}

// InsertTrendingRepo stores a single trending repository snapshot.
// Returns the row ID.
func (s *PostgresStore) InsertTrendingRepo(ctx context.Context, repo ossinsight.Repository, period string, language string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, insertTrendingRepoSQL+"\n\t\tRETURNING id",
		trendingRepoArgs(repo, period, language)...,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting trending repo: %w", classifyError(err))
	}
	return id, nil
}

// InsertTrendingRepos batch-inserts multiple trending repositories in a single
// transaction. Returns the number of rows inserted.
func (s *PostgresStore) InsertTrendingRepos(ctx context.Context, repos []ossinsight.Repository, period string, language string) (int64, error) {
	if len(repos) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, r := range repos {
		batch.Queue(insertTrendingRepoSQL, trendingRepoArgs(r, period, language)...)
	}

	affected, err := s.execBatch(ctx, batch, len(repos))
	if err != nil {
		return 0, fmt.Errorf("batch inserting trending repos: %w", classifyError(err))
	}
	return affected, nil
}

// GetTrendingRepoByID retrieves a single trending repo record by its database ID.
func (s *PostgresStore) GetTrendingRepoByID(ctx context.Context, id int64) (*TrendingRepoRecord, error) {
	var r TrendingRepoRecord
	err := s.pool.QueryRow(ctx,
		`SELECT `+trendingRepoColumns+` FROM trending_repos WHERE id = $1`, id,
	).Scan(scanTrendingRepoDest(&r)...)
	if err != nil {
		return nil, mapNotFound(err, "getting trending repo by ID")
	}
	return &r, nil
}

// ListTrendingRepos retrieves trending repos, optionally filtered by
// language, period, and time range. Supports cursor-based pagination.
func (s *PostgresStore) ListTrendingRepos(ctx context.Context, filter TrendingRepoFilter, opts ListOptions) ([]TrendingRepoRecord, error) {
	limit := clampLimit(opts.Limit)

	qb := queryBuilder{
		query: `SELECT ` + trendingRepoColumns + `
		FROM trending_repos
		WHERE ($1::bigint = 0 OR id < $1)`,
		args: []any{opts.Cursor},
	}

	qb.addIf(filter.Language != "", "trending_language", filter.Language)
	qb.addIf(filter.Period != "", "trending_period", filter.Period)
	qb.addIf(!filter.FetchedAfter.IsZero(), "fetched_at >", filter.FetchedAfter)
	qb.addIf(!filter.FetchedBefore.IsZero(), "fetched_at <", filter.FetchedBefore)

	query, args := qb.build("fetched_at DESC, id DESC", limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing trending repos: %w", err)
	}
	defer rows.Close()

	var repos []TrendingRepoRecord
	for rows.Next() {
		var r TrendingRepoRecord
		if err := rows.Scan(scanTrendingRepoDest(&r)...); err != nil {
			return nil, fmt.Errorf("scanning trending repo row: %w", err)
		}
		repos = append(repos, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating trending repo rows: %w", err)
	}
	return repos, nil
}

// DeleteTrendingRepo removes a trending repo record by its database ID.
// Returns true if a row was deleted, false if no row matched.
func (s *PostgresStore) DeleteTrendingRepo(ctx context.Context, id int64) (bool, error) {
	ct, err := s.pool.Exec(ctx, `DELETE FROM trending_repos WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("deleting trending repo: %w", err)
	}
	return ct.RowsAffected() > 0, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// execBatch runs a pgx.Batch inside a transaction, returning the total number
// of rows affected. The count parameter must match the number of queued
// statements in the batch.
func (s *PostgresStore) execBatch(ctx context.Context, batch *pgx.Batch, count int) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	br := tx.SendBatch(ctx, batch)
	var affected int64
	for range count {
		ct, err := br.Exec()
		if err != nil {
			br.Close()
			return 0, err
		}
		affected += ct.RowsAffected()
	}
	br.Close()

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("committing transaction: %w", err)
	}
	return affected, nil
}

// mapNotFound translates pgx.ErrNoRows to ErrNotFound and wraps all other
// errors with the given context message.
func mapNotFound(err error, context string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", context, err)
}

// clampLimit enforces the default and maximum pagination limits.
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// classifyError translates pgx-specific errors to sentinel errors.
func classifyError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrDuplicateKey, pgErr.Detail)
	}
	return err
}

// queryBuilder accumulates optional WHERE clauses with auto-numbered
// placeholders. It is not a general-purpose query builder -- just enough
// to keep the dynamic-filter pattern in ListTrendingRepos readable.
type queryBuilder struct {
	query string
	args  []any
}

// addIf appends " AND column = $N" (or a custom operator like ">", "<") when
// the condition is true. The clause string can be either a bare column name
// (implying "=") or "column op" (e.g. "fetched_at >").
func (qb *queryBuilder) addIf(cond bool, clause string, val any) {
	if !cond {
		return
	}
	argNum := len(qb.args) + 1
	// If the clause already contains an operator (space), use it as-is.
	// Otherwise default to "=".
	op := "="
	for i, c := range clause {
		if c == ' ' {
			op = clause[i+1:]
			clause = clause[:i]
			break
		}
	}
	qb.query += fmt.Sprintf(" AND %s %s $%d", clause, op, argNum)
	qb.args = append(qb.args, val)
}

// build finalises the query with ORDER BY and LIMIT clauses and returns
// the complete query string and argument slice.
func (qb *queryBuilder) build(orderBy string, limit int) (string, []any) {
	argNum := len(qb.args) + 1
	qb.query += fmt.Sprintf(" ORDER BY %s LIMIT $%d", orderBy, argNum)
	qb.args = append(qb.args, limit)
	return qb.query, qb.args
}
