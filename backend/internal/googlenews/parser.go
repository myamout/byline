// Package googlenews provides functionality for parsing Google News RSS feeds
// to extract news articles with their publishing source metadata.
package googlenews

import (
	"fmt"
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
