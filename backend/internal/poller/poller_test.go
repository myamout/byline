package poller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/myamout/byline/backend/internal/googlenews"
	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fakeSource implements Source with controllable behavior.
type fakeSource struct {
	name  string
	items []FeedItem
	err   error
	mu    sync.Mutex
	calls int
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Fetch(_ context.Context) ([]FeedItem, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.items, f.err
}

func (f *fakeSource) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// recordingStore wraps LogStore and records which persistence methods were called.
type recordingStore struct {
	*LogStore
	mu                 sync.Mutex
	upsertCalls        int
	upsertArticles     []reddit.Article
	insertCalls        int
	insertRepos        []ossinsight.Repository
	upsertNewsCalls    int
	upsertNewsArticles []googlenews.Article
}

func (s *recordingStore) UpsertArticles(ctx context.Context, articles []reddit.Article) (int64, error) {
	s.mu.Lock()
	s.upsertCalls++
	s.upsertArticles = append(s.upsertArticles, articles...)
	s.mu.Unlock()
	return s.LogStore.UpsertArticles(ctx, articles)
}

func (s *recordingStore) InsertTrendingRepos(ctx context.Context, repos []ossinsight.Repository, period, language string) (int64, error) {
	s.mu.Lock()
	s.insertCalls++
	s.insertRepos = append(s.insertRepos, repos...)
	s.mu.Unlock()
	return s.LogStore.InsertTrendingRepos(ctx, repos, period, language)
}

func (s *recordingStore) UpsertNewsArticles(ctx context.Context, articles []googlenews.Article) (int64, error) {
	s.mu.Lock()
	s.upsertNewsCalls++
	s.upsertNewsArticles = append(s.upsertNewsArticles, articles...)
	s.mu.Unlock()
	return s.LogStore.UpsertNewsArticles(ctx, articles)
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func redditFeedItems(n int) []FeedItem {
	items := make([]FeedItem, n)
	for i := range items {
		items[i] = FeedItem{
			Source:      SourceReddit,
			Title:       "article",
			URL:         "https://example.com/article",
			Author:      "user",
			PublishedAt: time.Now(),
			Metadata: map[string]string{
				"subreddit":   "golang",
				"reddit_link": "https://reddit.com/r/golang/abc",
			},
		}
	}
	return items
}

func ossinsightFeedItems(n int) []FeedItem {
	items := make([]FeedItem, n)
	for i := range items {
		items[i] = FeedItem{
			Source:      SourceOSSInsight,
			Title:       "org/repo",
			URL:         "https://github.com/org/repo",
			Description: "A repo",
			Metadata: map[string]string{
				"stars":             "100",
				"forks":             "10",
				"pull_requests":     "5",
				"total_score":       "200",
				"language":          "Go",
				"trending_period":   "past_week",
				"trending_language": "Go",
			},
		}
	}
	return items
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPoller_Run_HappyPath(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	src := &fakeSource{
		name:  "reddit",
		items: redditFeedItems(2),
	}

	p := NewBase(st, logger, 5*time.Second)
	p.AddSource(src, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	err := p.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
	}

	// With 10ms interval and 80ms runtime, we expect the initial fetch + several
	// ticker-driven fetches, so at least 2 calls.
	if calls := src.CallCount(); calls < 2 {
		t.Errorf("source was called %d times, want >= 2", calls)
	}
}

func TestPoller_Run_ContextCancellation(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	src := &fakeSource{
		name:  "reddit",
		items: redditFeedItems(1),
	}

	p := NewBase(st, logger, 5*time.Second)
	p.AddSource(src, 1*time.Hour) // Long interval so only the initial fetch fires.

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()

	// Let the initial fetch complete, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Run returned promptly after cancellation.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancellation")
	}
}

func TestPoller_Run_SourceError(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	src := &fakeSource{
		name: "reddit",
		err:  errors.New("network failure"),
	}

	p := NewBase(st, logger, 5*time.Second)
	p.AddSource(src, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := p.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
	}

	// Even though the source errors, the poller should keep calling it.
	if calls := src.CallCount(); calls < 2 {
		t.Errorf("source was called %d times despite errors, want >= 2", calls)
	}
}

func TestPoller_Run_EmptyItems(t *testing.T) {
	logger := newTestLogger()
	rec := &recordingStore{LogStore: NewLogStore(logger)}

	src := &fakeSource{
		name:  "reddit",
		items: nil, // No items returned.
	}

	p := NewBase(rec, logger, 5*time.Second)
	p.AddSource(src, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	// Source should have been called multiple times.
	if calls := src.CallCount(); calls < 2 {
		t.Errorf("source was called %d times, want >= 2", calls)
	}

	// No persistence calls should have been made.
	rec.mu.Lock()
	upserts := rec.upsertCalls
	inserts := rec.insertCalls
	rec.mu.Unlock()

	if upserts != 0 {
		t.Errorf("UpsertArticles called %d times, want 0", upserts)
	}
	if inserts != 0 {
		t.Errorf("InsertTrendingRepos called %d times, want 0", inserts)
	}
}

func TestPoller_Run_MultipleSources(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	redditSrc := &fakeSource{
		name:  "reddit",
		items: redditFeedItems(1),
	}
	ossSrc := &fakeSource{
		name:  "ossinsight",
		items: ossinsightFeedItems(1),
	}

	p := NewBase(st, logger, 5*time.Second)
	p.AddSource(redditSrc, 10*time.Millisecond)
	p.AddSource(ossSrc, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	if calls := redditSrc.CallCount(); calls < 2 {
		t.Errorf("reddit source was called %d times, want >= 2", calls)
	}
	if calls := ossSrc.CallCount(); calls < 2 {
		t.Errorf("ossinsight source was called %d times, want >= 2", calls)
	}
}

func TestPoller_Persist_RedditRouting(t *testing.T) {
	logger := newTestLogger()
	rec := &recordingStore{LogStore: NewLogStore(logger)}

	src := &fakeSource{
		name:  "reddit",
		items: redditFeedItems(2),
	}

	p := NewBase(rec, logger, 5*time.Second)
	p.AddSource(src, 1*time.Hour) // Long interval; only initial fetch fires.

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.upsertCalls == 0 {
		t.Fatal("UpsertArticles was never called")
	}
	if rec.insertCalls != 0 {
		t.Errorf("InsertTrendingRepos was called %d times, want 0 for reddit source", rec.insertCalls)
	}
	if len(rec.upsertArticles) != 2 {
		t.Errorf("UpsertArticles received %d articles, want 2", len(rec.upsertArticles))
	}
}

func TestPoller_Persist_OSSInsightRouting(t *testing.T) {
	logger := newTestLogger()
	rec := &recordingStore{LogStore: NewLogStore(logger)}

	src := &fakeSource{
		name:  "ossinsight",
		items: ossinsightFeedItems(3),
	}

	p := NewBase(rec, logger, 5*time.Second)
	p.AddSource(src, 1*time.Hour) // Long interval; only initial fetch fires.

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	rec.mu.Lock()
	defer rec.mu.Unlock()

	if rec.insertCalls == 0 {
		t.Fatal("InsertTrendingRepos was never called")
	}
	if rec.upsertCalls != 0 {
		t.Errorf("UpsertArticles was called %d times, want 0 for ossinsight source", rec.upsertCalls)
	}
	if len(rec.insertRepos) != 3 {
		t.Errorf("InsertTrendingRepos received %d repos, want 3", len(rec.insertRepos))
	}
}

func TestPoller_FetchTimeout_Default(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	cfg := Config{} // No FetchTimeout set.
	p := New(cfg, st, logger)

	if p.fetchTimeout != 30*time.Second {
		t.Errorf("fetchTimeout = %v, want 30s", p.fetchTimeout)
	}
}

func TestPoller_AddSource(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	src := &fakeSource{
		name:  "reddit",
		items: redditFeedItems(1),
	}

	p := NewBase(st, logger, 0)
	p.AddSource(src, 10*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = p.Run(ctx)

	if calls := src.CallCount(); calls < 1 {
		t.Errorf("source was called %d times, want >= 1", calls)
	}
}

func TestNew_NilConfigs(t *testing.T) {
	logger := newTestLogger()
	st := NewLogStore(logger)

	cfg := Config{
		Reddit:     nil,
		OSSInsight: nil,
	}
	p := New(cfg, st, logger)

	if len(p.runners) != 0 {
		t.Errorf("runners = %d, want 0 with nil configs", len(p.runners))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- p.Run(ctx)
	}()

	select {
	case err := <-done:
		// Run should return immediately since there are no sources.
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Errorf("Run() returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly with no sources")
	}
}

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
