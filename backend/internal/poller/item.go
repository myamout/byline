// Package poller provides a polling engine that periodically fetches content
// from configured sources and persists the results through a pluggable store.
package poller

import (
	"strconv"
	"time"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

// SourceType identifies the origin of a feed item.
type SourceType string

const (
	// SourceReddit indicates the item was fetched from a Reddit RSS feed.
	SourceReddit SourceType = "reddit"

	// SourceOSSInsight indicates the item was fetched from the OSSInsight API.
	SourceOSSInsight SourceType = "ossinsight"
)

// FeedItem is the normalized representation of a piece of content from any
// supported source. All source-specific data is mapped into this common
// structure so that downstream consumers do not need to know about individual
// source formats.
type FeedItem struct {
	// Source identifies which feed produced this item.
	Source SourceType

	// Title is the human-readable title of the content.
	Title string

	// URL is the canonical link to the content.
	URL string

	// Description is an optional summary or body excerpt.
	Description string

	// Author is the creator or submitter of the content.
	Author string

	// Tags is a set of labels used for categorization (e.g. subreddit, language).
	Tags []string

	// PublishedAt is the time the content was originally published.
	PublishedAt time.Time

	// FetchedAt is the time this item was retrieved by the poller.
	FetchedAt time.Time

	// Metadata holds arbitrary key-value pairs specific to the source.
	Metadata map[string]string
}

// toFeedItem converts a reddit.Article into a normalized FeedItem.
func toFeedItem(a reddit.Article) FeedItem {
	return FeedItem{
		Source:      SourceReddit,
		Title:       a.Title,
		URL:         a.ArticleLink,
		Author:      a.Author,
		Tags:        []string{a.Subreddit},
		PublishedAt: a.PostedAt,
		FetchedAt:   time.Now(),
		Metadata: map[string]string{
			"subreddit":   a.Subreddit,
			"reddit_link": a.RedditLink,
		},
	}
}

// repoToFeedItem converts an ossinsight.Repository into a normalized FeedItem.
func repoToFeedItem(r ossinsight.Repository) FeedItem {
	return FeedItem{
		Source:      SourceOSSInsight,
		Title:       r.RepoName,
		URL:         "https://github.com/" + r.RepoName,
		Description: r.Description,
		Tags:        []string{r.PrimaryLanguage},
		FetchedAt:   time.Now(),
		Metadata: map[string]string{
			"stars":         strconv.Itoa(r.Stars),
			"forks":         strconv.Itoa(r.Forks),
			"pull_requests": strconv.Itoa(r.PullRequests),
			"total_score":   strconv.Itoa(r.TotalScore),
			"language":      r.PrimaryLanguage,
		},
	}
}

// feedItemToArticle converts a FeedItem back to a reddit.Article for persistence.
func feedItemToArticle(item FeedItem) reddit.Article {
	return reddit.Article{
		Author:      item.Author,
		Subreddit:   item.Metadata["subreddit"],
		PostedAt:    item.PublishedAt,
		ArticleLink: item.URL,
		Title:       item.Title,
		RedditLink:  item.Metadata["reddit_link"],
	}
}

// feedItemToRepository converts a FeedItem back to an ossinsight.Repository for persistence.
func feedItemToRepository(item FeedItem) ossinsight.Repository {
	stars, _ := strconv.Atoi(item.Metadata["stars"])
	forks, _ := strconv.Atoi(item.Metadata["forks"])
	prs, _ := strconv.Atoi(item.Metadata["pull_requests"])
	score, _ := strconv.Atoi(item.Metadata["total_score"])
	return ossinsight.Repository{
		RepoName:        item.Title,
		PrimaryLanguage: item.Metadata["language"],
		Description:     item.Description,
		Stars:           stars,
		Forks:           forks,
		PullRequests:    prs,
		TotalScore:      score,
	}
}
