package poller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/myamout/byline/backend/internal/googlenews"
	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
	"github.com/myamout/byline/backend/internal/store"
)

// Poller periodically fetches content from registered sources and persists
// the results via a store.Store implementation.
type Poller struct {
	runners      []sourceRunner
	store        store.Store
	logger       *slog.Logger
	fetchTimeout time.Duration
}

type sourceRunner struct {
	source   Source
	interval time.Duration
}

// New creates a Poller from the given config, store, and logger.
// It builds source runners from the config -- nil Reddit/OSSInsight configs
// disable those sources.
func New(cfg Config, st store.Store, logger *slog.Logger) *Poller {
	p := &Poller{
		store:        st,
		logger:       logger,
		fetchTimeout: cfg.FetchTimeout,
	}

	if p.fetchTimeout == 0 {
		p.fetchTimeout = 30 * time.Second
	}

	if cfg.Reddit != nil {
		interval := cfg.Reddit.Interval
		if interval == 0 {
			interval = 5 * time.Minute
		}
		src := NewRedditSource(reddit.NewParser(), cfg.Reddit.Subreddits)
		p.runners = append(p.runners, sourceRunner{source: src, interval: interval})
	}

	if cfg.OSSInsight != nil {
		interval := cfg.OSSInsight.Interval
		if interval == 0 {
			interval = 1 * time.Hour
		}
		src := NewOSSInsightSource(ossinsight.NewClient(), cfg.OSSInsight.Queries)
		p.runners = append(p.runners, sourceRunner{source: src, interval: interval})
	}

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

	return p
}

// NewBase creates a Poller with just a store and logger, without any sources.
// Use AddSource to register sources. This is useful for testing.
func NewBase(st store.Store, logger *slog.Logger, fetchTimeout time.Duration) *Poller {
	if fetchTimeout == 0 {
		fetchTimeout = 30 * time.Second
	}
	return &Poller{
		store:        st,
		logger:       logger,
		fetchTimeout: fetchTimeout,
	}
}

// AddSource registers a source with the given polling interval.
// This is useful for testing with custom Source implementations.
func (p *Poller) AddSource(src Source, interval time.Duration) {
	p.runners = append(p.runners, sourceRunner{source: src, interval: interval})
}

// Run starts polling all configured sources and blocks until ctx is cancelled.
// Each source runs in its own goroutine with an independent ticker.
func (p *Poller) Run(ctx context.Context) error {
	var wg sync.WaitGroup

	for _, r := range p.runners {
		wg.Add(1)
		go func(r sourceRunner) {
			defer wg.Done()
			p.runSource(ctx, r)
		}(r)
	}

	wg.Wait()
	return ctx.Err()
}

func (p *Poller) runSource(ctx context.Context, r sourceRunner) {
	// Fetch immediately on startup.
	p.fetchAndStore(ctx, r.source)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("source stopping", "source", r.source.Name())
			return
		case <-ticker.C:
			p.fetchAndStore(ctx, r.source)
		}
	}
}

func (p *Poller) fetchAndStore(ctx context.Context, src Source) {
	fetchCtx, cancel := context.WithTimeout(ctx, p.fetchTimeout)
	defer cancel()

	items, err := src.Fetch(fetchCtx)
	if err != nil {
		p.logger.Error("fetch failed", "source", src.Name(), "error", err)
		return
	}

	if len(items) == 0 {
		p.logger.Debug("no items returned", "source", src.Name())
		return
	}

	if err := p.persist(ctx, src, items); err != nil {
		p.logger.Error("persist failed", "source", src.Name(), "error", err)
		return
	}

	p.logger.Info("items stored", "source", src.Name(), "count", len(items))
}

// persist dispatches to the correct store method based on source type.
func (p *Poller) persist(ctx context.Context, src Source, items []FeedItem) error {
	switch src.Name() {
	case "reddit":
		articles := make([]reddit.Article, 0, len(items))
		for _, item := range items {
			articles = append(articles, feedItemToArticle(item))
		}
		n, err := p.store.UpsertArticles(ctx, articles)
		if err != nil {
			return fmt.Errorf("upserting reddit articles: %w", err)
		}
		p.logger.Debug("reddit upsert complete", "rows_affected", n)
		return nil

	case "ossinsight":
		repos := make([]ossinsight.Repository, 0, len(items))
		var period, language string
		for _, item := range items {
			repos = append(repos, feedItemToRepository(item))
			if period == "" {
				period = item.Metadata["trending_period"]
			}
			if language == "" {
				language = item.Metadata["trending_language"]
			}
		}
		n, err := p.store.InsertTrendingRepos(ctx, repos, period, language)
		if err != nil {
			return fmt.Errorf("inserting trending repos: %w", err)
		}
		p.logger.Debug("trending repos insert complete", "rows_inserted", n)
		return nil

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

	default:
		return fmt.Errorf("unknown source type: %s", src.Name())
	}
}
