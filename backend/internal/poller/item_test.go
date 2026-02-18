package poller

import (
	"testing"
	"time"

	"github.com/myamout/byline/backend/internal/ossinsight"
	"github.com/myamout/byline/backend/internal/reddit"
)

func TestToFeedItem(t *testing.T) {
	posted := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	article := reddit.Article{
		Author:      "gopher42",
		Subreddit:   "golang",
		PostedAt:    posted,
		ArticleLink: "https://example.com/cool-article",
		Title:       "Cool Go Article",
		RedditLink:  "https://www.reddit.com/r/golang/comments/abc123",
	}

	item := toFeedItem(article)

	if item.Source != SourceReddit {
		t.Errorf("Source = %q, want %q", item.Source, SourceReddit)
	}
	if item.Title != article.Title {
		t.Errorf("Title = %q, want %q", item.Title, article.Title)
	}
	if item.URL != article.ArticleLink {
		t.Errorf("URL = %q, want %q", item.URL, article.ArticleLink)
	}
	if item.Author != article.Author {
		t.Errorf("Author = %q, want %q", item.Author, article.Author)
	}
	if !item.PublishedAt.Equal(posted) {
		t.Errorf("PublishedAt = %v, want %v", item.PublishedAt, posted)
	}

	// Tags should contain exactly the subreddit.
	if len(item.Tags) != 1 || item.Tags[0] != "golang" {
		t.Errorf("Tags = %v, want [golang]", item.Tags)
	}

	// Metadata keys.
	if item.Metadata["subreddit"] != "golang" {
		t.Errorf("Metadata[subreddit] = %q, want %q", item.Metadata["subreddit"], "golang")
	}
	if item.Metadata["reddit_link"] != article.RedditLink {
		t.Errorf("Metadata[reddit_link] = %q, want %q", item.Metadata["reddit_link"], article.RedditLink)
	}
}

func TestRepoToFeedItem(t *testing.T) {
	repo := ossinsight.Repository{
		RepoID:          12345,
		RepoName:        "myorg/myrepo",
		PrimaryLanguage: "Go",
		Description:     "An awesome repository",
		Stars:           1500,
		Forks:           200,
		PullRequests:    50,
		TotalScore:      9000,
	}

	item := repoToFeedItem(repo)

	if item.Source != SourceOSSInsight {
		t.Errorf("Source = %q, want %q", item.Source, SourceOSSInsight)
	}
	if item.Title != repo.RepoName {
		t.Errorf("Title = %q, want %q", item.Title, repo.RepoName)
	}
	if want := "https://github.com/myorg/myrepo"; item.URL != want {
		t.Errorf("URL = %q, want %q", item.URL, want)
	}
	if item.Description != repo.Description {
		t.Errorf("Description = %q, want %q", item.Description, repo.Description)
	}

	// Tags should contain the primary language.
	if len(item.Tags) != 1 || item.Tags[0] != "Go" {
		t.Errorf("Tags = %v, want [Go]", item.Tags)
	}

	// Verify all expected metadata keys and values.
	expectedMeta := map[string]string{
		"stars":         "1500",
		"forks":         "200",
		"pull_requests": "50",
		"total_score":   "9000",
		"language":      "Go",
	}
	for k, want := range expectedMeta {
		if got := item.Metadata[k]; got != want {
			t.Errorf("Metadata[%s] = %q, want %q", k, got, want)
		}
	}
}

func TestToFeedItem_FetchedAtIsSet(t *testing.T) {
	before := time.Now()

	article := reddit.Article{
		Title:       "Test",
		ArticleLink: "https://example.com",
	}
	item := toFeedItem(article)

	after := time.Now()

	if item.FetchedAt.Before(before) || item.FetchedAt.After(after) {
		t.Errorf("FetchedAt = %v, want between %v and %v", item.FetchedAt, before, after)
	}

	if time.Since(item.FetchedAt) > time.Second {
		t.Errorf("FetchedAt is too far in the past: %v ago", time.Since(item.FetchedAt))
	}
}

func TestRepoToFeedItem_FetchedAtIsSet(t *testing.T) {
	before := time.Now()

	repo := ossinsight.Repository{
		RepoName: "owner/repo",
	}
	item := repoToFeedItem(repo)

	after := time.Now()

	if item.FetchedAt.Before(before) || item.FetchedAt.After(after) {
		t.Errorf("FetchedAt = %v, want between %v and %v", item.FetchedAt, before, after)
	}

	if time.Since(item.FetchedAt) > time.Second {
		t.Errorf("FetchedAt is too far in the past: %v ago", time.Since(item.FetchedAt))
	}
}

func TestFeedItemToArticle(t *testing.T) {
	posted := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	item := FeedItem{
		Source:      SourceReddit,
		Title:       "Cool Go Article",
		URL:         "https://example.com/cool-article",
		Author:      "gopher42",
		PublishedAt: posted,
		Metadata: map[string]string{
			"subreddit":   "golang",
			"reddit_link": "https://www.reddit.com/r/golang/comments/abc123",
		},
	}

	article := feedItemToArticle(item)

	if article.Author != "gopher42" {
		t.Errorf("Author = %q, want %q", article.Author, "gopher42")
	}
	if article.Subreddit != "golang" {
		t.Errorf("Subreddit = %q, want %q", article.Subreddit, "golang")
	}
	if !article.PostedAt.Equal(posted) {
		t.Errorf("PostedAt = %v, want %v", article.PostedAt, posted)
	}
	if article.ArticleLink != "https://example.com/cool-article" {
		t.Errorf("ArticleLink = %q, want %q", article.ArticleLink, "https://example.com/cool-article")
	}
	if article.Title != "Cool Go Article" {
		t.Errorf("Title = %q, want %q", article.Title, "Cool Go Article")
	}
	if article.RedditLink != "https://www.reddit.com/r/golang/comments/abc123" {
		t.Errorf("RedditLink = %q, want %q", article.RedditLink, "https://www.reddit.com/r/golang/comments/abc123")
	}
}

func TestFeedItemToRepository(t *testing.T) {
	item := FeedItem{
		Source:      SourceOSSInsight,
		Title:       "myorg/myrepo",
		URL:         "https://github.com/myorg/myrepo",
		Description: "An awesome repository",
		Metadata: map[string]string{
			"stars":         "1500",
			"forks":         "200",
			"pull_requests": "50",
			"total_score":   "9000",
			"language":      "Go",
		},
	}

	repo := feedItemToRepository(item)

	if repo.RepoName != "myorg/myrepo" {
		t.Errorf("RepoName = %q, want %q", repo.RepoName, "myorg/myrepo")
	}
	if repo.PrimaryLanguage != "Go" {
		t.Errorf("PrimaryLanguage = %q, want %q", repo.PrimaryLanguage, "Go")
	}
	if repo.Description != "An awesome repository" {
		t.Errorf("Description = %q, want %q", repo.Description, "An awesome repository")
	}
	if repo.Stars != 1500 {
		t.Errorf("Stars = %d, want %d", repo.Stars, 1500)
	}
	if repo.Forks != 200 {
		t.Errorf("Forks = %d, want %d", repo.Forks, 200)
	}
	if repo.PullRequests != 50 {
		t.Errorf("PullRequests = %d, want %d", repo.PullRequests, 50)
	}
	if repo.TotalScore != 9000 {
		t.Errorf("TotalScore = %d, want %d", repo.TotalScore, 9000)
	}
}

func TestFeedItemToRepository_MissingMetadata(t *testing.T) {
	item := FeedItem{
		Source:      SourceOSSInsight,
		Title:       "org/repo",
		Description: "No numeric metadata",
		Metadata:    map[string]string{},
	}

	repo := feedItemToRepository(item)

	if repo.RepoName != "org/repo" {
		t.Errorf("RepoName = %q, want %q", repo.RepoName, "org/repo")
	}
	if repo.PrimaryLanguage != "" {
		t.Errorf("PrimaryLanguage = %q, want empty string", repo.PrimaryLanguage)
	}
	if repo.Stars != 0 {
		t.Errorf("Stars = %d, want 0", repo.Stars)
	}
	if repo.Forks != 0 {
		t.Errorf("Forks = %d, want 0", repo.Forks)
	}
	if repo.PullRequests != 0 {
		t.Errorf("PullRequests = %d, want 0", repo.PullRequests)
	}
	if repo.TotalScore != 0 {
		t.Errorf("TotalScore = %d, want 0", repo.TotalScore)
	}
}
