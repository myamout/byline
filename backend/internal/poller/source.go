package poller

import "context"

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
