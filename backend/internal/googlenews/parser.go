// Package googlenews provides functionality for parsing Google News RSS feeds
// to extract news articles with their publishing source metadata.
package googlenews

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/mmcdole/gofeed/rss"
)

// Article represents a news article from a Google News RSS feed.
type Article struct {
	// Title is the headline of the article.
	Title string

	// URL is the Google News redirect URL for the article.
	URL string

	// Source is the name of the publishing source (e.g., "The New York Times").
	Source string

	// SourceURL is the URL of the publishing source (e.g., "https://www.nytimes.com").
	SourceURL string

	// PublishedAt is the time when the article was published.
	PublishedAt time.Time
}

// Parser parses Google News RSS feeds to extract articles.
type Parser struct {
	// feedParser is the underlying gofeed parser configured with a custom
	// RSS translator that preserves source element data.
	feedParser *gofeed.Parser

	// httpClient is used for HTTP requests when fetching feeds by URL.
	httpClient *http.Client
}

// NewParser creates a new Google News RSS feed parser with a default
// HTTP client configured with a 10-second timeout.
func NewParser() *Parser {
	fp := gofeed.NewParser()
	fp.RSSTranslator = &sourcePreservingTranslator{}

	return &Parser{
		feedParser: fp,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewParserWithClient creates a new Google News RSS feed parser with the
// provided HTTP client. This is useful for testing with httptest servers
// or when custom HTTP transport configuration is needed.
func NewParserWithClient(client *http.Client) *Parser {
	fp := gofeed.NewParser()
	fp.RSSTranslator = &sourcePreservingTranslator{}

	return &Parser{
		feedParser: fp,
		httpClient: client,
	}
}

// ParseFeed fetches a Google News RSS feed from the given URL, extracts
// articles, and resolves Google redirect URLs to the actual article URLs.
// Articles whose redirect resolution fails are silently skipped (logged
// but not returned).
func (p *Parser) ParseFeed(ctx context.Context, feedURL string) ([]Article, error) {
	feed, err := p.feedParser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feed from %s: %w", feedURL, err)
	}

	raw := p.extractArticles(feed)
	articles := make([]Article, 0, len(raw))

	for _, a := range raw {
		resolved, err := p.resolveURL(ctx, a.URL)
		if err != nil {
			log.Printf("googlenews: skipping article %q: resolve redirect: %v", a.Title, err)
			continue
		}
		a.URL = resolved
		articles = append(articles, a)
	}

	return articles, nil
}

// errRedirectCaptured is a sentinel error used internally by resolveURL
// to stop the HTTP client from following redirects. When the CheckRedirect
// function captures the final redirect Location, it returns this error to
// halt the redirect chain.
var errRedirectCaptured = errors.New("redirect captured")

// resolveURL follows redirects from a Google News URL to determine the
// final article URL. It issues an HTTP HEAD request and uses a custom
// CheckRedirect function to capture the target of the redirect chain
// without actually following it to the (potentially unreachable) external
// host. If the URL returns a 2xx status with no redirect, the original URL
// is returned. Non-2xx responses (e.g. 500) produce an error.
func (p *Parser) resolveURL(ctx context.Context, googleURL string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var finalURL string

	// Create a shallow copy of the HTTP client so the custom CheckRedirect
	// does not affect the shared client used for feed fetching.
	client := *p.httpClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		finalURL = req.URL.String()
		return errRedirectCaptured
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, googleURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		// If the error wraps our sentinel, the redirect was captured successfully.
		if errors.Is(err, errRedirectCaptured) && finalURL != "" {
			return finalURL, nil
		}
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// No redirect occurred. If the status is 2xx, return the original URL.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return googleURL, nil
	}

	return "", fmt.Errorf("unexpected status %d for %s", resp.StatusCode, googleURL)
}

// ParseString parses a Google News RSS feed from a string and returns
// the extracted articles. No redirect resolution is performed on URLs.
// This is useful for testing or when the feed content is already available.
func (p *Parser) ParseString(feedContent string) ([]Article, error) {
	feed, err := p.feedParser.ParseString(feedContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse feed content: %w", err)
	}

	return p.extractArticles(feed), nil
}

// extractArticles iterates over feed items and converts each into an Article.
func (p *Parser) extractArticles(feed *gofeed.Feed) []Article {
	articles := make([]Article, 0, len(feed.Items))
	for _, item := range feed.Items {
		articles = append(articles, p.parseItem(item))
	}
	return articles
}

// parseItem extracts article information from a single feed item.
func (p *Parser) parseItem(item *gofeed.Item) Article {
	sourceName, sourceURL := extractSource(item)

	var publishedAt time.Time
	if item.PublishedParsed != nil {
		publishedAt = *item.PublishedParsed
	}

	return Article{
		Title:       item.Title,
		URL:         item.Link,
		Source:      sourceName,
		SourceURL:   sourceURL,
		PublishedAt: publishedAt,
	}
}

// extractSource extracts the publisher name and URL from the RSS <source>
// element. In gofeed, this data is preserved by our custom translator in the
// item's Custom map with keys "source" (name) and "sourceURL" (url attribute).
// Returns empty strings if no source element was present.
func extractSource(item *gofeed.Item) (name, url string) {
	if item.Custom == nil {
		return "", ""
	}
	return item.Custom["source"], item.Custom["sourceURL"]
}

// sourcePreservingTranslator is a custom gofeed RSS translator that extends
// the default translator to preserve the RSS <source> element data.
//
// The default gofeed translator parses <source> into rss.Item.Source but
// does not map it to any field on the universal gofeed.Item. This translator
// writes the source name and URL into the Custom map so it can be accessed
// after translation.
type sourcePreservingTranslator struct {
	defaultTranslator gofeed.DefaultRSSTranslator
}

// Translate converts an rss.Feed into the universal gofeed.Feed,
// preserving the <source> element data in each item's Custom map.
func (t *sourcePreservingTranslator) Translate(feed interface{}) (*gofeed.Feed, error) {
	rssFeed, ok := feed.(*rss.Feed)
	if !ok {
		return nil, fmt.Errorf("feed did not match expected type of *rss.Feed")
	}

	// Build a lookup of source data keyed by item index before translation,
	// since the default translator will discard it.
	type sourceInfo struct {
		title string
		url   string
	}
	sources := make([]sourceInfo, len(rssFeed.Items))
	for i, item := range rssFeed.Items {
		if item.Source != nil {
			sources[i] = sourceInfo{title: item.Source.Title, url: item.Source.URL}
		}
	}

	// Delegate to the default translator for all standard field mapping.
	result, err := t.defaultTranslator.Translate(feed)
	if err != nil {
		return nil, err
	}

	// Inject the source data into each translated item's Custom map.
	for i, item := range result.Items {
		if i < len(sources) && sources[i].title != "" {
			if item.Custom == nil {
				item.Custom = make(map[string]string)
			}
			item.Custom["source"] = sources[i].title
			item.Custom["sourceURL"] = sources[i].url
		}
	}

	return result, nil
}
