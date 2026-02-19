package poller

import (
	"context"
	"log/slog"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
	"github.com/myamout/byline/backend/internal/store"
)

// Compile-time check that LogStore implements store.Store.
var _ store.Store = (*LogStore)(nil)

// LogStore is a store.Store implementation that logs items via slog.
// It is intended for development and testing without a database.
type LogStore struct {
	logger *slog.Logger
}

// NewLogStore creates a LogStore that writes to the provided logger.
func NewLogStore(logger *slog.Logger) *LogStore {
	return &LogStore{logger: logger}
}

func (s *LogStore) UpsertArticle(_ context.Context, article reddit.Article) (int64, error) {
	s.logger.Info("upsert article", "title", article.Title, "link", article.ArticleLink)
	return 1, nil
}

func (s *LogStore) UpsertArticles(_ context.Context, articles []reddit.Article) (int64, error) {
	for _, a := range articles {
		s.logger.Info("upsert article", "title", a.Title, "link", a.ArticleLink)
	}
	return int64(len(articles)), nil
}

func (s *LogStore) GetArticleByID(_ context.Context, _ int64) (*reddit.Article, error) {
	return nil, store.ErrNotFound
}

func (s *LogStore) ListArticles(_ context.Context, _ string, _ store.ListOptions) ([]reddit.Article, error) {
	return nil, nil
}

func (s *LogStore) DeleteArticle(_ context.Context, _ int64) (bool, error) {
	return false, nil
}

func (s *LogStore) InsertTrendingRepo(_ context.Context, repo ossinsight.Repository, period string, _ string) (int64, error) {
	s.logger.Info("insert trending repo", "repo", repo.RepoName, "period", period)
	return 1, nil
}

func (s *LogStore) InsertTrendingRepos(_ context.Context, repos []ossinsight.Repository, period string, language string) (int64, error) {
	for _, r := range repos {
		s.logger.Info("insert trending repo", "repo", r.RepoName, "period", period, "language", language)
	}
	return int64(len(repos)), nil
}

func (s *LogStore) GetTrendingRepoByID(_ context.Context, _ int64) (*store.TrendingRepoRecord, error) {
	return nil, store.ErrNotFound
}

func (s *LogStore) ListTrendingRepos(_ context.Context, _ store.TrendingRepoFilter, _ store.ListOptions) ([]store.TrendingRepoRecord, error) {
	return nil, nil
}

func (s *LogStore) DeleteTrendingRepo(_ context.Context, _ int64) (bool, error) {
	return false, nil
}

func (s *LogStore) Ping(_ context.Context) error {
	return nil
}

func (s *LogStore) Close() {}
