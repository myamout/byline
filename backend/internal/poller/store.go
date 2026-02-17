package poller

import (
	"context"
	"log/slog"
)

// Store persists feed items retrieved by the poller.
// Implementations must be safe for concurrent use.
type Store interface {
	// Save persists the given items. It returns an error if any item could
	// not be stored.
	Save(ctx context.Context, items []FeedItem) error
}

// LogStore is a Store implementation that logs each item using the standard
// slog package. It is intended for development and testing; it does not
// perform any durable persistence.
type LogStore struct {
	logger *slog.Logger
}

// NewLogStore creates a LogStore that writes to the provided logger.
func NewLogStore(logger *slog.Logger) *LogStore {
	return &LogStore{logger: logger}
}

// Save logs each item at Info level and always returns nil.
func (s *LogStore) Save(_ context.Context, items []FeedItem) error {
	for _, item := range items {
		s.logger.Info("feed item",
			"source", string(item.Source),
			"title", item.Title,
			"url", item.URL,
		)
	}
	return nil
}
