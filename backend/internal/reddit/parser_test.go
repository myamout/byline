package reddit

import (
	"testing"
	"time"
)

// Sample Reddit Atom feed for testing.
const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <category term="golang" label="r/golang"/>
  <updated>2024-01-15T12:00:00+00:00</updated>
  <title>r/golang</title>
  <subtitle>Ask questions and post articles about the Go programming language.</subtitle>
  <id>/r/golang</id>
  <link rel="alternate" href="https://www.reddit.com/r/golang/" />

  <entry>
    <author><name>/u/gopher123</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;table&gt;&lt;tr&gt;&lt;td&gt;submitted by &lt;a href="https://www.reddit.com/user/gopher123"&gt;gopher123&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;tr&gt;&lt;td&gt;&lt;a href="https://go.dev/blog/example"&gt;[link]&lt;/a&gt;&lt;/td&gt;&lt;td&gt;&lt;a href="https://www.reddit.com/r/golang/comments/abc123"&gt;[comments]&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;/table&gt;</content>
    <id>t3_abc123</id>
    <link href="https://www.reddit.com/r/golang/comments/abc123/new_go_feature/" />
    <updated>2024-01-15T10:30:00+00:00</updated>
    <title>New Go Feature Announcement</title>
  </entry>

  <entry>
    <author><name>/u/selfposter</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;table&gt;&lt;tr&gt;&lt;td&gt;submitted by &lt;a href="https://www.reddit.com/user/selfposter"&gt;selfposter&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;tr&gt;&lt;td&gt;&lt;a href="https://www.reddit.com/r/golang/comments/def456/question_about_channels/"&gt;[link]&lt;/a&gt;&lt;/td&gt;&lt;td&gt;&lt;a href="https://www.reddit.com/r/golang/comments/def456"&gt;[comments]&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;/table&gt;</content>
    <id>t3_def456</id>
    <link href="https://www.reddit.com/r/golang/comments/def456/question_about_channels/" />
    <updated>2024-01-15T09:00:00+00:00</updated>
    <title>Question about channels</title>
  </entry>

  <entry>
    <author><name>/u/newssharer</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;table&gt;&lt;tr&gt;&lt;td&gt;submitted by &lt;a href="https://www.reddit.com/user/newssharer"&gt;newssharer&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;tr&gt;&lt;td&gt;&lt;a href="https://blog.golang.org/new-release"&gt;[link]&lt;/a&gt;&lt;/td&gt;&lt;td&gt;&lt;a href="https://www.reddit.com/r/golang/comments/ghi789"&gt;[comments]&lt;/a&gt;&lt;/td&gt;&lt;/tr&gt;&lt;/table&gt;</content>
    <id>t3_ghi789</id>
    <link href="https://www.reddit.com/r/golang/comments/ghi789/go_121_released/" />
    <updated>2024-01-14T15:45:00+00:00</updated>
    <title>Go 1.21 Released!</title>
  </entry>
</feed>`

func TestParseString(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	// Should have 2 articles (the self-post should be filtered out).
	if len(articles) != 2 {
		t.Errorf("Expected 2 articles, got %d", len(articles))
	}
}

func TestParseString_FiltersSelfPosts(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	// Verify no self-posts are included.
	for _, article := range articles {
		if p.isSelfPost(article.ArticleLink) {
			t.Errorf("Self-post not filtered: %s", article.ArticleLink)
		}
	}
}

func TestParseString_ExtractsArticleData(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) < 1 {
		t.Fatal("Expected at least 1 article")
	}

	// Check the first article (gopher123's post).
	article := articles[0]

	if article.Author != "gopher123" {
		t.Errorf("Expected author 'gopher123', got '%s'", article.Author)
	}

	if article.Subreddit != "golang" {
		t.Errorf("Expected subreddit 'golang', got '%s'", article.Subreddit)
	}

	if article.ArticleLink != "https://go.dev/blog/example" {
		t.Errorf("Expected article link 'https://go.dev/blog/example', got '%s'", article.ArticleLink)
	}

	if article.Title != "New Go Feature Announcement" {
		t.Errorf("Expected title 'New Go Feature Announcement', got '%s'", article.Title)
	}

	expectedTime := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	if !article.PostedAt.Equal(expectedTime) {
		t.Errorf("Expected PostedAt %v, got %v", expectedTime, article.PostedAt)
	}
}

func TestExtractArticleLink(t *testing.T) {
	p := NewParser()

	tests := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "standard link",
			content:  `<a href="https://example.com/article">[link]</a>`,
			expected: "https://example.com/article",
		},
		{
			name:     "link with extra attributes",
			content:  `<a href="https://example.com/article" target="_blank">[link]</a>`,
			expected: "https://example.com/article",
		},
		{
			name:     "link with whitespace",
			content:  `<a href="https://example.com/article"> [link] </a>`,
			expected: "https://example.com/article",
		},
		{
			name:     "no link tag",
			content:  `<a href="https://example.com/article">[comments]</a>`,
			expected: "",
		},
		{
			name:     "empty content",
			content:  "",
			expected: "",
		},
		{
			name:     "link in larger HTML",
			content:  `<table><tr><td>text</td></tr><tr><td><a href="https://blog.example.com/post">[link]</a></td></tr></table>`,
			expected: "https://blog.example.com/post",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.extractArticleLink(tt.content)
			if result != tt.expected {
				t.Errorf("extractArticleLink() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsSelfPost(t *testing.T) {
	p := NewParser()

	tests := []struct {
		name     string
		link     string
		expected bool
	}{
		{
			name:     "external link",
			link:     "https://go.dev/blog/example",
			expected: false,
		},
		{
			name:     "reddit.com link",
			link:     "https://www.reddit.com/r/golang/comments/abc123",
			expected: true,
		},
		{
			name:     "reddit.com without www",
			link:     "https://reddit.com/r/golang/comments/abc123",
			expected: true,
		},
		{
			name:     "redd.it short link",
			link:     "https://redd.it/abc123",
			expected: true,
		},
		{
			name:     "old.reddit.com",
			link:     "https://old.reddit.com/r/golang/comments/abc123",
			expected: true,
		},
		{
			name:     "external with reddit in path",
			link:     "https://example.com/article-about-reddit",
			expected: false,
		},
		{
			name:     "case insensitive",
			link:     "https://REDDIT.COM/r/golang/comments/abc123",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.isSelfPost(tt.link)
			if result != tt.expected {
				t.Errorf("isSelfPost(%s) = %v, want %v", tt.link, result, tt.expected)
			}
		})
	}
}

func TestCleanAuthorName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "with /u/ prefix",
			input:    "/u/gopher123",
			expected: "gopher123",
		},
		{
			name:     "without prefix",
			input:    "gopher123",
			expected: "gopher123",
		},
		{
			name:     "with whitespace",
			input:    "  /u/gopher123  ",
			expected: "gopher123",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanAuthorName(tt.input)
			if result != tt.expected {
				t.Errorf("cleanAuthorName(%s) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNewParser(t *testing.T) {
	p := NewParser()

	if p == nil {
		t.Fatal("NewParser() returned nil")
	}

	if p.feedParser == nil {
		t.Error("feedParser is nil")
	}

	if p.linkRegex == nil {
		t.Error("linkRegex is nil")
	}
}

func TestParseString_EmptyFeed(t *testing.T) {
	p := NewParser()

	emptyFeed := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>r/golang</title>
</feed>`

	articles, err := p.ParseString(emptyFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) != 0 {
		t.Errorf("Expected 0 articles for empty feed, got %d", len(articles))
	}
}

func TestParseString_InvalidFeed(t *testing.T) {
	p := NewParser()

	_, err := p.ParseString("not valid xml")
	if err == nil {
		t.Error("Expected error for invalid feed, got nil")
	}
}

func TestArticle_ZeroTime(t *testing.T) {
	p := NewParser()

	// Feed without time information.
	feedWithoutTime := `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>r/golang</title>
  <entry>
    <author><name>/u/gopher123</name></author>
    <category term="golang" label="r/golang"/>
    <content type="html">&lt;a href="https://example.com/article"&gt;[link]&lt;/a&gt;</content>
    <id>t3_abc123</id>
    <link href="https://www.reddit.com/r/golang/comments/abc123/" />
    <title>Test Post</title>
  </entry>
</feed>`

	articles, err := p.ParseString(feedWithoutTime)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) != 1 {
		t.Fatalf("Expected 1 article, got %d", len(articles))
	}

	if !articles[0].PostedAt.IsZero() {
		t.Errorf("Expected zero time, got %v", articles[0].PostedAt)
	}
}
