package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/poller"
	"github.com/myamout/byline/backend/internal/store"
)

func main() {
	// Set up structured logging.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Set up context with signal handling for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Read DATABASE_URL. If not set, fall back to LogStore for development.
	databaseURL := os.Getenv("DATABASE_URL")

	var st store.Store
	if databaseURL != "" {
		pgStore, err := store.NewPostgresStore(ctx, databaseURL)
		if err != nil {
			logger.Error("failed to connect to database", "error", err)
			os.Exit(1)
		}
		defer pgStore.Close()
		st = pgStore
		logger.Info("connected to database")
	} else {
		logger.Warn("DATABASE_URL not set, using LogStore (no persistence)")
		st = poller.NewLogStore(logger)
	}

	// Build poller config.
	cfg := poller.Config{
		Reddit: &poller.RedditConfig{
			Subreddits: []string{"golang", "rust", "programming"},
			Interval:   5 * time.Minute,
		},
		OSSInsight: &poller.OSSInsightConfig{
			Queries: []ossinsight.TrendingReposOptions{
				{Period: ossinsight.PeriodPast24Hours},
			},
			Interval: 1 * time.Hour,
		},
		GoogleNews: &poller.GoogleNewsConfig{
			FeedURL:  "https://news.google.com/rss?hl=en-US&gl=US&ceid=US:en",
			Interval: 30 * time.Minute,
		},
		FetchTimeout: 30 * time.Second,
	}

	p := poller.New(cfg, st, logger)

	logger.Info("poller starting",
		"subreddits", cfg.Reddit.Subreddits,
		"reddit_interval", cfg.Reddit.Interval,
		"ossinsight_interval", cfg.OSSInsight.Interval,
		"googlenews_feed", cfg.GoogleNews.FeedURL,
		"googlenews_interval", cfg.GoogleNews.Interval,
	)

	if err := p.Run(ctx); err != nil && err != context.Canceled {
		logger.Error("poller exited with error", "error", err)
		os.Exit(1)
	}

	logger.Info("poller shut down cleanly")
}
