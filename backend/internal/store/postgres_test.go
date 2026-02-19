package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/myamout/byline/backend/internal/googlenews"
	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// testDSN reads the database URL from the environment.
// If the variable is not set the test is skipped so the suite compiles
// and passes go vet even without a live Postgres instance.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("BYLINE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BYLINE_TEST_DATABASE_URL not set; skipping integration test")
	}
	return dsn
}

// setupTestStore creates a PostgresStore for testing. It runs migrations
// programmatically using the embedded SQL files, then truncates both tables
// on cleanup so each test starts with a clean slate.
func setupTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := testDSN(t)
	ctx := context.Background()

	// Run migrations before creating the store so the schema is current.
	runTestMigrations(t, dsn)

	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}

	// Truncate tables before each test for isolation (run at start too).
	_, err = s.pool.Exec(ctx, "TRUNCATE reddit_articles, trending_repos, news_articles RESTART IDENTITY CASCADE")
	if err != nil {
		s.Close()
		t.Fatalf("truncating tables: %v", err)
	}

	t.Cleanup(func() {
		cleanCtx := context.Background()
		_, _ = s.pool.Exec(cleanCtx, "TRUNCATE reddit_articles, trending_repos, news_articles RESTART IDENTITY CASCADE")
		s.Close()
	})

	return s
}

// runTestMigrations applies all pending migrations using the embedded SQL files.
func runTestMigrations(t *testing.T, dsn string) {
	t.Helper()

	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("creating iofs source: %v", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", source, "pgx5://"+dsn[len("postgres://"):])
	if err != nil {
		t.Fatalf("creating migrator: %v", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("running migrations: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helper factories
// ---------------------------------------------------------------------------

func makeArticle(subreddit, link, title string) reddit.Article {
	return reddit.Article{
		Author:      "testauthor",
		Subreddit:   subreddit,
		PostedAt:    time.Now().Truncate(time.Microsecond).UTC(),
		ArticleLink: link,
		Title:       title,
		RedditLink:  "https://reddit.com/r/" + subreddit + "/comments/abc",
	}
}

func makeRepo(id int, name, lang string, score int) ossinsight.Repository {
	return ossinsight.Repository{
		RepoID:            id,
		RepoName:          name,
		PrimaryLanguage:   lang,
		Description:       fmt.Sprintf("description for %s", name),
		Stars:             score * 10,
		Forks:             score * 2,
		PullRequests:      score,
		Pushes:            score * 3,
		TotalScore:        score,
		ContributorLogins: "user1,user2",
		CollectionNames:   "collection1",
	}
}

func makeNewsArticle(url, title, source string) googlenews.Article {
	return googlenews.Article{
		Title:       title,
		URL:         url,
		Source:      source,
		SourceURL:   "https://www." + strings.ToLower(strings.ReplaceAll(source, " ", "")) + ".com",
		PublishedAt: time.Now().Truncate(time.Microsecond).UTC(),
	}
}

// ---------------------------------------------------------------------------
// Reddit Articles
// ---------------------------------------------------------------------------

func TestUpsertArticle_Insert(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeArticle("golang", "https://example.com/article1", "Test Article 1")
	id, err := s.UpsertArticle(ctx, a)
	if err != nil {
		t.Fatalf("UpsertArticle: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected id > 0, got %d", id)
	}

	got, err := s.GetArticleByID(ctx, id)
	if err != nil {
		t.Fatalf("GetArticleByID: %v", err)
	}
	if got.Title != a.Title {
		t.Errorf("title: got %q, want %q", got.Title, a.Title)
	}
	if got.Author != a.Author {
		t.Errorf("author: got %q, want %q", got.Author, a.Author)
	}
	if got.Subreddit != a.Subreddit {
		t.Errorf("subreddit: got %q, want %q", got.Subreddit, a.Subreddit)
	}
	if got.ArticleLink != a.ArticleLink {
		t.Errorf("article_link: got %q, want %q", got.ArticleLink, a.ArticleLink)
	}
	if got.RedditLink != a.RedditLink {
		t.Errorf("reddit_link: got %q, want %q", got.RedditLink, a.RedditLink)
	}
	if !got.PostedAt.Equal(a.PostedAt) {
		t.Errorf("posted_at: got %v, want %v", got.PostedAt, a.PostedAt)
	}
}

func TestUpsertArticle_Update(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeArticle("golang", "https://example.com/article-upsert", "Original Title")
	id1, err := s.UpsertArticle(ctx, a)
	if err != nil {
		t.Fatalf("first UpsertArticle: %v", err)
	}

	a.Title = "Updated Title"
	id2, err := s.UpsertArticle(ctx, a)
	if err != nil {
		t.Fatalf("second UpsertArticle: %v", err)
	}
	if id1 != id2 {
		t.Errorf("expected same id after upsert: got %d and %d", id1, id2)
	}

	got, err := s.GetArticleByID(ctx, id1)
	if err != nil {
		t.Fatalf("GetArticleByID: %v", err)
	}
	if got.Title != "Updated Title" {
		t.Errorf("title not updated: got %q, want %q", got.Title, "Updated Title")
	}
}

func TestUpsertArticles_Batch(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	articles := make([]reddit.Article, 10)
	for i := range articles {
		articles[i] = makeArticle("golang", fmt.Sprintf("https://example.com/batch-%d", i), fmt.Sprintf("Batch Article %d", i))
	}

	count, err := s.UpsertArticles(ctx, articles)
	if err != nil {
		t.Fatalf("UpsertArticles: %v", err)
	}
	if count != 10 {
		t.Errorf("expected 10 rows affected, got %d", count)
	}

	// Verify all articles are retrievable.
	listed, err := s.ListArticles(ctx, "golang", ListOptions{Limit: 20})
	if err != nil {
		t.Fatalf("ListArticles: %v", err)
	}
	if len(listed) != 10 {
		t.Errorf("expected 10 articles listed, got %d", len(listed))
	}
}

func TestUpsertArticles_EmptySlice(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	count, err := s.UpsertArticles(ctx, nil)
	if err != nil {
		t.Fatalf("UpsertArticles(nil): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows affected for nil, got %d", count)
	}

	count, err = s.UpsertArticles(ctx, []reddit.Article{})
	if err != nil {
		t.Fatalf("UpsertArticles(empty): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows affected for empty slice, got %d", count)
	}
}

func TestGetArticleByID_NotFound(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	_, err := s.GetArticleByID(ctx, 999999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListArticles_Pagination(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Insert 15 articles with staggered posted_at times so ordering is deterministic.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 15 {
		a := makeArticle("golang", fmt.Sprintf("https://example.com/page-%d", i), fmt.Sprintf("Page Article %d", i))
		a.PostedAt = base.Add(time.Duration(i) * time.Hour)
		if _, err := s.UpsertArticle(ctx, a); err != nil {
			t.Fatalf("UpsertArticle[%d]: %v", i, err)
		}
	}

	// Fetch all IDs in the expected order so we can compute cursors.
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM reddit_articles
		WHERE subreddit = 'golang'
		ORDER BY posted_at DESC, id DESC
	`)
	if err != nil {
		t.Fatalf("query IDs: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 15 {
		t.Fatalf("expected 15 ids, got %d", len(ids))
	}

	// Page 1: first 5 articles (most recent).
	page1, err := s.ListArticles(ctx, "golang", ListOptions{Limit: 5, Cursor: 0})
	if err != nil {
		t.Fatalf("ListArticles page1: %v", err)
	}
	if len(page1) != 5 {
		t.Fatalf("page1: expected 5 articles, got %d", len(page1))
	}

	// Verify descending order: each article's posted_at should be >= next.
	for i := 1; i < len(page1); i++ {
		if page1[i].PostedAt.After(page1[i-1].PostedAt) {
			t.Errorf("page1 not in descending order at index %d", i)
		}
	}

	// Page 2: cursor is the ID of the last item on page 1 (index 4).
	cursor := ids[4]
	page2, err := s.ListArticles(ctx, "golang", ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListArticles page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2: expected 5 articles, got %d", len(page2))
	}

	// Page 3: cursor is the ID of the last item on page 2 (index 9).
	cursor = ids[9]
	page3, err := s.ListArticles(ctx, "golang", ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListArticles page3: %v", err)
	}
	if len(page3) != 5 {
		t.Fatalf("page3: expected 5 articles, got %d", len(page3))
	}

	// Page 4: cursor is the last ID; should return 0 articles.
	cursor = ids[14]
	page4, err := s.ListArticles(ctx, "golang", ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListArticles page4: %v", err)
	}
	if len(page4) != 0 {
		t.Errorf("page4: expected 0 articles, got %d", len(page4))
	}

	// Total collected across pages should be 15 unique articles.
	total := len(page1) + len(page2) + len(page3)
	if total != 15 {
		t.Errorf("total articles across pages: got %d, want 15", total)
	}

	// Verify no overlap between pages by checking titles are distinct.
	seen := make(map[string]bool)
	for _, page := range [][]reddit.Article{page1, page2, page3} {
		for _, a := range page {
			if seen[a.Title] {
				t.Errorf("duplicate article across pages: %q", a.Title)
			}
			seen[a.Title] = true
		}
	}
}

func TestListArticles_EmptySubreddit(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Insert an article in a different subreddit to ensure filtering works.
	a := makeArticle("golang", "https://example.com/some-article", "Some Article")
	if _, err := s.UpsertArticle(ctx, a); err != nil {
		t.Fatalf("UpsertArticle: %v", err)
	}

	// Query a subreddit with no data.
	results, err := s.ListArticles(ctx, "nonexistent_subreddit", ListOptions{})
	if err != nil {
		t.Fatalf("ListArticles: %v", err)
	}
	if results == nil {
		// The implementation returns nil for empty results; both nil and empty
		// slice are acceptable. What matters is no error and length 0.
	}
	if len(results) != 0 {
		t.Errorf("expected 0 articles for empty subreddit, got %d", len(results))
	}
}

func TestDeleteArticle_Exists(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeArticle("golang", "https://example.com/delete-me", "Delete Me")
	id, err := s.UpsertArticle(ctx, a)
	if err != nil {
		t.Fatalf("UpsertArticle: %v", err)
	}

	deleted, err := s.DeleteArticle(ctx, id)
	if err != nil {
		t.Fatalf("DeleteArticle: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}

	// Verify it is gone.
	_, err = s.GetArticleByID(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteArticle_NotExists(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	deleted, err := s.DeleteArticle(ctx, 999999)
	if err != nil {
		t.Fatalf("DeleteArticle: %v", err)
	}
	if deleted {
		t.Error("expected deleted=false for non-existent row")
	}
}

// ---------------------------------------------------------------------------
// Trending Repositories
// ---------------------------------------------------------------------------

func TestInsertTrendingRepo_Success(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	repo := makeRepo(1001, "owner/repo1", "Go", 42)
	id, err := s.InsertTrendingRepo(ctx, repo, "past_24_hours", "Go")
	if err != nil {
		t.Fatalf("InsertTrendingRepo: %v", err)
	}
	if id <= 0 {
		t.Fatalf("expected id > 0, got %d", id)
	}

	got, err := s.GetTrendingRepoByID(ctx, id)
	if err != nil {
		t.Fatalf("GetTrendingRepoByID: %v", err)
	}
	if got.RepoName != repo.RepoName {
		t.Errorf("repo_name: got %q, want %q", got.RepoName, repo.RepoName)
	}
	if got.RepoID != repo.RepoID {
		t.Errorf("repo_id: got %d, want %d", got.RepoID, repo.RepoID)
	}
	if got.PrimaryLanguage != repo.PrimaryLanguage {
		t.Errorf("primary_language: got %q, want %q", got.PrimaryLanguage, repo.PrimaryLanguage)
	}
	if got.Description != repo.Description {
		t.Errorf("description: got %q, want %q", got.Description, repo.Description)
	}
	if got.Stars != repo.Stars {
		t.Errorf("stars: got %d, want %d", got.Stars, repo.Stars)
	}
	if got.Forks != repo.Forks {
		t.Errorf("forks: got %d, want %d", got.Forks, repo.Forks)
	}
	if got.PullRequests != repo.PullRequests {
		t.Errorf("pull_requests: got %d, want %d", got.PullRequests, repo.PullRequests)
	}
	if got.Pushes != repo.Pushes {
		t.Errorf("pushes: got %d, want %d", got.Pushes, repo.Pushes)
	}
	if got.TotalScore != repo.TotalScore {
		t.Errorf("total_score: got %d, want %d", got.TotalScore, repo.TotalScore)
	}
	if got.ContributorLogins != repo.ContributorLogins {
		t.Errorf("contributor_logins: got %q, want %q", got.ContributorLogins, repo.ContributorLogins)
	}
	if got.CollectionNames != repo.CollectionNames {
		t.Errorf("collection_names: got %q, want %q", got.CollectionNames, repo.CollectionNames)
	}
	if got.TrendingPeriod != "past_24_hours" {
		t.Errorf("trending_period: got %q, want %q", got.TrendingPeriod, "past_24_hours")
	}
	if got.TrendingLanguage != "Go" {
		t.Errorf("trending_language: got %q, want %q", got.TrendingLanguage, "Go")
	}
	if got.FetchedAt.IsZero() {
		t.Error("fetched_at should not be zero")
	}
	if got.CreatedAt.IsZero() {
		t.Error("created_at should not be zero")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("updated_at should not be zero")
	}
}

func TestInsertTrendingRepos_Batch(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	repos := make([]ossinsight.Repository, 20)
	for i := range repos {
		repos[i] = makeRepo(2000+i, fmt.Sprintf("owner/batch-repo-%d", i), "Go", 10+i)
	}

	count, err := s.InsertTrendingRepos(ctx, repos, "past_week", "Go")
	if err != nil {
		t.Fatalf("InsertTrendingRepos: %v", err)
	}
	if count != 20 {
		t.Errorf("expected 20 rows inserted, got %d", count)
	}

	// Verify all repos are retrievable via list.
	listed, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{Language: "Go", Period: "past_week"}, ListOptions{Limit: 50})
	if err != nil {
		t.Fatalf("ListTrendingRepos: %v", err)
	}
	if len(listed) != 20 {
		t.Errorf("expected 20 repos listed, got %d", len(listed))
	}
}

func TestListTrendingRepos_FilterByLanguage(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	goRepos := []ossinsight.Repository{
		makeRepo(3001, "owner/go-repo-1", "Go", 50),
		makeRepo(3002, "owner/go-repo-2", "Go", 60),
	}
	rustRepos := []ossinsight.Repository{
		makeRepo(3003, "owner/rust-repo-1", "Rust", 70),
	}

	if _, err := s.InsertTrendingRepos(ctx, goRepos, "past_24_hours", "Go"); err != nil {
		t.Fatalf("InsertTrendingRepos (Go): %v", err)
	}
	if _, err := s.InsertTrendingRepos(ctx, rustRepos, "past_24_hours", "Rust"); err != nil {
		t.Fatalf("InsertTrendingRepos (Rust): %v", err)
	}

	// Filter by Go.
	results, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{Language: "Go"}, ListOptions{})
	if err != nil {
		t.Fatalf("ListTrendingRepos (Go): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 Go repos, got %d", len(results))
	}
	for _, r := range results {
		if r.TrendingLanguage != "Go" {
			t.Errorf("expected trending_language=Go, got %q", r.TrendingLanguage)
		}
	}

	// Filter by Rust.
	results, err = s.ListTrendingRepos(ctx, TrendingRepoFilter{Language: "Rust"}, ListOptions{})
	if err != nil {
		t.Fatalf("ListTrendingRepos (Rust): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 Rust repo, got %d", len(results))
	}
	if results[0].TrendingLanguage != "Rust" {
		t.Errorf("expected trending_language=Rust, got %q", results[0].TrendingLanguage)
	}
}

func TestListTrendingRepos_FilterByPeriod(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	dailyRepos := []ossinsight.Repository{
		makeRepo(4001, "owner/daily-1", "Go", 10),
		makeRepo(4002, "owner/daily-2", "Go", 20),
	}
	weeklyRepos := []ossinsight.Repository{
		makeRepo(4003, "owner/weekly-1", "Go", 30),
	}

	if _, err := s.InsertTrendingRepos(ctx, dailyRepos, "past_24_hours", "Go"); err != nil {
		t.Fatalf("InsertTrendingRepos (daily): %v", err)
	}
	if _, err := s.InsertTrendingRepos(ctx, weeklyRepos, "past_week", "Go"); err != nil {
		t.Fatalf("InsertTrendingRepos (weekly): %v", err)
	}

	// Filter by daily.
	results, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{Period: "past_24_hours"}, ListOptions{})
	if err != nil {
		t.Fatalf("ListTrendingRepos (daily): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 daily repos, got %d", len(results))
	}
	for _, r := range results {
		if r.TrendingPeriod != "past_24_hours" {
			t.Errorf("expected trending_period=past_24_hours, got %q", r.TrendingPeriod)
		}
	}

	// Filter by weekly.
	results, err = s.ListTrendingRepos(ctx, TrendingRepoFilter{Period: "past_week"}, ListOptions{})
	if err != nil {
		t.Fatalf("ListTrendingRepos (weekly): %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 weekly repo, got %d", len(results))
	}
	if results[0].TrendingPeriod != "past_week" {
		t.Errorf("expected trending_period=past_week, got %q", results[0].TrendingPeriod)
	}
}

func TestListTrendingRepos_FilterByTimeRange(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Insert repos with explicit fetched_at timestamps via raw SQL so we
	// control the time values precisely.
	now := time.Now().Truncate(time.Microsecond).UTC()
	oneHourAgo := now.Add(-1 * time.Hour)
	twoHoursAgo := now.Add(-2 * time.Hour)
	threeHoursAgo := now.Add(-3 * time.Hour)

	insertWithFetchedAt := func(repoID int, name string, fetchedAt time.Time) int64 {
		t.Helper()
		var id int64
		err := s.pool.QueryRow(ctx, `
			INSERT INTO trending_repos (
				repo_id, repo_name, primary_language, description,
				stars, forks, pull_requests, pushes, total_score,
				contributor_logins, collection_names,
				trending_period, trending_language, fetched_at
			)
			VALUES ($1, $2, 'Go', 'desc', 100, 10, 5, 15, 50, 'user1', 'col1',
				'past_24_hours', 'Go', $3)
			RETURNING id
		`, repoID, name, fetchedAt).Scan(&id)
		if err != nil {
			t.Fatalf("insertWithFetchedAt: %v", err)
		}
		return id
	}

	insertWithFetchedAt(6001, "owner/old-repo", threeHoursAgo)
	insertWithFetchedAt(6002, "owner/mid-repo", twoHoursAgo)
	insertWithFetchedAt(6003, "owner/recent-repo", oneHourAgo)
	insertWithFetchedAt(6004, "owner/newest-repo", now)

	// Filter: fetched after 2.5 hours ago (should get mid, recent, newest).
	cutoff := now.Add(-2*time.Hour - 30*time.Minute)
	results, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{
		FetchedAfter: cutoff,
	}, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListTrendingRepos (FetchedAfter): %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("FetchedAfter: expected 3 repos, got %d", len(results))
	}

	// Filter: fetched before 1.5 hours ago (should get old, mid).
	cutoff = now.Add(-1*time.Hour - 30*time.Minute)
	results, err = s.ListTrendingRepos(ctx, TrendingRepoFilter{
		FetchedBefore: cutoff,
	}, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListTrendingRepos (FetchedBefore): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("FetchedBefore: expected 2 repos, got %d", len(results))
	}

	// Filter: fetched between 2.5 hours ago and 30 minutes ago (should get mid, recent).
	results, err = s.ListTrendingRepos(ctx, TrendingRepoFilter{
		FetchedAfter:  now.Add(-2*time.Hour - 30*time.Minute),
		FetchedBefore: now.Add(-30 * time.Minute),
	}, ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("ListTrendingRepos (time range): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("time range: expected 2 repos, got %d", len(results))
	}
}

func TestListTrendingRepos_Pagination(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Insert 12 repos.
	repos := make([]ossinsight.Repository, 12)
	for i := range repos {
		repos[i] = makeRepo(7000+i, fmt.Sprintf("owner/page-repo-%d", i), "Go", 100+i)
	}
	count, err := s.InsertTrendingRepos(ctx, repos, "past_24_hours", "Go")
	if err != nil {
		t.Fatalf("InsertTrendingRepos: %v", err)
	}
	if count != 12 {
		t.Fatalf("expected 12 rows inserted, got %d", count)
	}

	// Fetch all IDs in expected order to compute cursors.
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM trending_repos
		ORDER BY fetched_at DESC, id DESC
	`)
	if err != nil {
		t.Fatalf("query IDs: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 12 {
		t.Fatalf("expected 12 ids, got %d", len(ids))
	}

	// Page 1: first 5 repos.
	page1, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{}, ListOptions{Limit: 5, Cursor: 0})
	if err != nil {
		t.Fatalf("ListTrendingRepos page1: %v", err)
	}
	if len(page1) != 5 {
		t.Fatalf("page1: expected 5 repos, got %d", len(page1))
	}

	// Page 2: cursor is the ID of the last item on page 1 (index 4).
	cursor := ids[4]
	page2, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{}, ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListTrendingRepos page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2: expected 5 repos, got %d", len(page2))
	}

	// Page 3: remaining 2 repos.
	cursor = ids[9]
	page3, err := s.ListTrendingRepos(ctx, TrendingRepoFilter{}, ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListTrendingRepos page3: %v", err)
	}
	if len(page3) != 2 {
		t.Fatalf("page3: expected 2 repos, got %d", len(page3))
	}

	// Total across pages should be 12.
	total := len(page1) + len(page2) + len(page3)
	if total != 12 {
		t.Errorf("total repos across pages: got %d, want 12", total)
	}

	// Verify no overlap by checking repo IDs are distinct.
	seen := make(map[int64]bool)
	for _, page := range [][]TrendingRepoRecord{page1, page2, page3} {
		for _, r := range page {
			if seen[r.ID] {
				t.Errorf("duplicate repo across pages: ID %d", r.ID)
			}
			seen[r.ID] = true
		}
	}
}

func TestDeleteTrendingRepo(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	repo := makeRepo(5001, "owner/delete-me", "Go", 10)
	id, err := s.InsertTrendingRepo(ctx, repo, "past_24_hours", "Go")
	if err != nil {
		t.Fatalf("InsertTrendingRepo: %v", err)
	}

	deleted, err := s.DeleteTrendingRepo(ctx, id)
	if err != nil {
		t.Fatalf("DeleteTrendingRepo: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}

	// Verify it is gone.
	_, err = s.GetTrendingRepoByID(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete again -- should return false.
	deleted, err = s.DeleteTrendingRepo(ctx, id)
	if err != nil {
		t.Fatalf("DeleteTrendingRepo (second): %v", err)
	}
	if deleted {
		t.Error("expected deleted=false for already-deleted row")
	}
}

// ---------------------------------------------------------------------------
// News Articles
// ---------------------------------------------------------------------------

func TestUpsertNewsArticle_Insert(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeNewsArticle("https://example.com/news1", "Test News Article 1", "The Example Times")
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

	a := makeNewsArticle("https://example.com/news-upsert", "Original News Title", "The Example Times")
	id1, err := s.UpsertNewsArticle(ctx, a)
	if err != nil {
		t.Fatalf("first UpsertNewsArticle: %v", err)
	}

	a.Title = "Updated News Title"
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
	if got.Title != "Updated News Title" {
		t.Errorf("title not updated: got %q, want %q", got.Title, "Updated News Title")
	}
}

func TestUpsertNewsArticles_Batch(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	articles := make([]googlenews.Article, 10)
	for i := range articles {
		articles[i] = makeNewsArticle(
			fmt.Sprintf("https://example.com/news-batch-%d", i),
			fmt.Sprintf("Batch News Article %d", i),
			"The Example Times",
		)
	}

	count, err := s.UpsertNewsArticles(ctx, articles)
	if err != nil {
		t.Fatalf("UpsertNewsArticles: %v", err)
	}
	if count != 10 {
		t.Errorf("expected 10 rows affected, got %d", count)
	}

	// Verify all articles are retrievable.
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
		t.Errorf("expected 0 rows affected for nil, got %d", count)
	}

	count, err = s.UpsertNewsArticles(ctx, []googlenews.Article{})
	if err != nil {
		t.Fatalf("UpsertNewsArticles(empty): %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows affected for empty slice, got %d", count)
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

	// Insert 12 articles with staggered published_at times so ordering is deterministic.
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 12 {
		a := makeNewsArticle(
			fmt.Sprintf("https://example.com/news-page-%d", i),
			fmt.Sprintf("Page News Article %d", i),
			"The Example Times",
		)
		a.PublishedAt = base.Add(time.Duration(i) * time.Hour)
		if _, err := s.UpsertNewsArticle(ctx, a); err != nil {
			t.Fatalf("UpsertNewsArticle[%d]: %v", i, err)
		}
	}

	// Fetch all IDs in the expected order so we can compute cursors.
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM news_articles
		ORDER BY published_at DESC, id DESC
	`)
	if err != nil {
		t.Fatalf("query IDs: %v", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 12 {
		t.Fatalf("expected 12 ids, got %d", len(ids))
	}

	// Page 1: first 5 articles (most recent).
	page1, err := s.ListNewsArticles(ctx, ListOptions{Limit: 5, Cursor: 0})
	if err != nil {
		t.Fatalf("ListNewsArticles page1: %v", err)
	}
	if len(page1) != 5 {
		t.Fatalf("page1: expected 5 articles, got %d", len(page1))
	}

	// Verify descending order: each article's published_at should be >= next.
	for i := 1; i < len(page1); i++ {
		if page1[i].PublishedAt.After(page1[i-1].PublishedAt) {
			t.Errorf("page1 not in descending order at index %d", i)
		}
	}

	// Page 2: cursor is the ID of the last item on page 1 (index 4).
	cursor := ids[4]
	page2, err := s.ListNewsArticles(ctx, ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListNewsArticles page2: %v", err)
	}
	if len(page2) != 5 {
		t.Fatalf("page2: expected 5 articles, got %d", len(page2))
	}

	// Page 3: remaining 2 articles.
	cursor = ids[9]
	page3, err := s.ListNewsArticles(ctx, ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListNewsArticles page3: %v", err)
	}
	if len(page3) != 2 {
		t.Fatalf("page3: expected 2 articles, got %d", len(page3))
	}

	// Page 4: cursor is the last ID; should return 0 articles.
	cursor = ids[11]
	page4, err := s.ListNewsArticles(ctx, ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("ListNewsArticles page4: %v", err)
	}
	if len(page4) != 0 {
		t.Errorf("page4: expected 0 articles, got %d", len(page4))
	}

	// Total collected across pages should be 12 unique articles.
	total := len(page1) + len(page2) + len(page3)
	if total != 12 {
		t.Errorf("total articles across pages: got %d, want 12", total)
	}

	// Verify no overlap between pages by checking titles are distinct.
	seen := make(map[string]bool)
	for _, page := range [][]googlenews.Article{page1, page2, page3} {
		for _, a := range page {
			if seen[a.Title] {
				t.Errorf("duplicate article across pages: %q", a.Title)
			}
			seen[a.Title] = true
		}
	}
}

func TestDeleteNewsArticle(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	a := makeNewsArticle("https://example.com/news-delete-me", "Delete Me", "The Example Times")
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

	// Verify it is gone.
	_, err = s.GetNewsArticleByID(ctx, id)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete again -- should return false.
	deleted, err = s.DeleteNewsArticle(ctx, id)
	if err != nil {
		t.Fatalf("DeleteNewsArticle (second): %v", err)
	}
	if deleted {
		t.Error("expected deleted=false for already-deleted row")
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestPing(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestNewPostgresStore_InvalidConnString(t *testing.T) {
	// This test does not need a running database -- it only checks that an
	// invalid DSN is rejected. We still gate on the env var so it only runs
	// in the integration test environment.
	testDSN(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := NewPostgresStore(ctx, "postgres://invalid:invalid@localhost:1/nonexistent?connect_timeout=1")
	if err == nil {
		t.Fatal("expected error for invalid connection string, got nil")
	}
}
