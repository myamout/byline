// Package reddit provides functionality for parsing Reddit RSS feeds
// to extract external article links shared by users.
package reddit

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// Article represents an external article shared on Reddit.
// It contains metadata about the post and the link to the external content.
type Article struct {
	// Author is the Reddit username who posted the article (e.g., "username").
	Author string

	// Subreddit is the subreddit where the article was posted (e.g., "golang").
	Subreddit string

	// PostedAt is the time when the article was posted to Reddit.
	PostedAt time.Time

	// ArticleLink is the URL to the external article.
	ArticleLink string

	// Title is the title of the Reddit post.
	Title string

	// RedditLink is the URL to the Reddit comments page.
	RedditLink string
}

// Parser parses Reddit RSS feeds to extract external articles.
type Parser struct {
	// feedParser is the underlying gofeed parser.
	feedParser *gofeed.Parser

	// linkRegex extracts the [link] href from HTML content.
	linkRegex *regexp.Regexp
}

// NewParser creates a new Reddit RSS feed parser.
func NewParser() *Parser {
	fp := gofeed.NewParser()
	// Set a reasonable user agent for Reddit API compliance.
	fp.UserAgent = "byline/1.0 (RSS Feed Parser)"

	return &Parser{
		feedParser: fp,
		// Match <a href="URL">[link]</a> pattern in Reddit's HTML content.
		// The URL is captured in group 1.
		linkRegex: regexp.MustCompile(`<a\s+href="([^"]+)"[^>]*>\s*\[link\]\s*</a>`),
	}
}

// ParseSubreddit fetches and parses the RSS feed for a given subreddit,
// returning only posts that link to external articles (not self-posts).
func (p *Parser) ParseSubreddit(ctx context.Context, subreddit string) ([]Article, error) {
	// Normalize subreddit name (remove leading r/ if present).
	subreddit = strings.TrimPrefix(subreddit, "r/")
	subreddit = strings.TrimPrefix(subreddit, "/r/")

	feedURL := fmt.Sprintf("https://www.reddit.com/r/%s/.rss", subreddit)
	return p.ParseURL(ctx, feedURL)
}

// ParseURL fetches and parses a Reddit RSS feed from the given URL,
// returning only posts that link to external articles (not self-posts).
func (p *Parser) ParseURL(ctx context.Context, feedURL string) ([]Article, error) {
	feed, err := p.feedParser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch feed from %s: %w", feedURL, err)
	}

	return p.extractArticles(feed)
}

// ParseString parses a Reddit RSS feed from a string,
// returning only posts that link to external articles (not self-posts).
// This is useful for testing or when the feed content is already available.
func (p *Parser) ParseString(feedContent string) ([]Article, error) {
	feed, err := p.feedParser.ParseString(feedContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse feed content: %w", err)
	}

	return p.extractArticles(feed)
}

// extractArticles processes feed items and extracts external articles.
func (p *Parser) extractArticles(feed *gofeed.Feed) ([]Article, error) {
	var articles []Article

	for _, item := range feed.Items {
		article, ok := p.parseItem(item, feed)
		if !ok {
			// Skip self-posts or items without valid external links.
			continue
		}
		articles = append(articles, article)
	}

	return articles, nil
}

// parseItem extracts article information from a single feed item.
// Returns the Article and true if successful, or zero value and false
// if the item is a self-post or cannot be parsed.
func (p *Parser) parseItem(item *gofeed.Item, feed *gofeed.Feed) (Article, bool) {
	// Extract the external article link from the HTML content.
	articleLink := p.extractArticleLink(item.Content)
	if articleLink == "" {
		return Article{}, false
	}

	// Filter out self-posts (links pointing back to reddit.com).
	if p.isSelfPost(articleLink) {
		return Article{}, false
	}

	// Extract author name from the feed item.
	author := p.extractAuthor(item)

	// Extract subreddit from categories.
	subreddit := p.extractSubreddit(item, feed)

	// Get the posted time.
	postedAt := p.extractTime(item)

	return Article{
		Author:      author,
		Subreddit:   subreddit,
		PostedAt:    postedAt,
		ArticleLink: articleLink,
		Title:       item.Title,
		RedditLink:  item.Link,
	}, true
}

// extractArticleLink extracts the external article URL from the HTML content.
// Reddit RSS feeds include the article link in the content as: <a href="URL">[link]</a>
func (p *Parser) extractArticleLink(content string) string {
	matches := p.linkRegex.FindStringSubmatch(content)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// isSelfPost checks if the article link points back to Reddit (indicating a self-post).
func (p *Parser) isSelfPost(articleLink string) bool {
	link := strings.ToLower(articleLink)
	return strings.Contains(link, "reddit.com") ||
		strings.Contains(link, "redd.it") ||
		strings.Contains(link, "redditgifts.com") ||
		strings.Contains(link, "reddit.app")
}

// extractAuthor extracts the author username from the feed item.
// Reddit uses the format "/u/username" in the author name field.
func (p *Parser) extractAuthor(item *gofeed.Item) string {
	// Try the Authors slice first (preferred).
	if len(item.Authors) > 0 && item.Authors[0] != nil {
		return cleanAuthorName(item.Authors[0].Name)
	}

	// Fall back to deprecated Author field.
	if item.Author != nil {
		return cleanAuthorName(item.Author.Name)
	}

	return ""
}

// cleanAuthorName removes the "/u/" prefix from Reddit usernames.
func cleanAuthorName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/u/")
	return name
}

// extractSubreddit extracts the subreddit name from the feed item categories.
// Reddit includes the subreddit as a category with label "r/subreddit".
func (p *Parser) extractSubreddit(item *gofeed.Item, feed *gofeed.Feed) string {
	// Check item categories first.
	for _, cat := range item.Categories {
		if strings.HasPrefix(cat, "r/") {
			return strings.TrimPrefix(cat, "r/")
		}
		// Sometimes the category is just the subreddit name.
		if cat != "" && !strings.Contains(cat, "/") {
			return cat
		}
	}

	// Fall back to feed categories.
	for _, cat := range feed.Categories {
		if strings.HasPrefix(cat, "r/") {
			return strings.TrimPrefix(cat, "r/")
		}
	}

	// Try to extract from feed title (e.g., "r/golang").
	if strings.HasPrefix(feed.Title, "r/") {
		return strings.TrimPrefix(feed.Title, "r/")
	}

	return ""
}

// extractTime extracts the posted time from the feed item.
// Prefers UpdatedParsed (from Atom <updated>), falls back to PublishedParsed.
func (p *Parser) extractTime(item *gofeed.Item) time.Time {
	// Reddit Atom feeds use <updated> for the post time.
	if item.UpdatedParsed != nil {
		return *item.UpdatedParsed
	}

	if item.PublishedParsed != nil {
		return *item.PublishedParsed
	}

	return time.Time{}
}
