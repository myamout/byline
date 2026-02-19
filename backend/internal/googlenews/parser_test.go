package googlenews

import (
	"testing"
	"time"
)

const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <link>https://news.google.com</link>
    <description>Google News</description>
    <item>
      <title>Breaking: Major Event Unfolds - The New York Times</title>
      <link>https://news.google.com/rss/articles/CBMiXX123</link>
      <guid isPermaLink="false">CBMiXX123</guid>
      <pubDate>Wed, 19 Feb 2026 10:30:00 GMT</pubDate>
      <description>&lt;ol&gt;&lt;li&gt;cluster content&lt;/li&gt;&lt;/ol&gt;</description>
      <source url="https://www.nytimes.com">The New York Times</source>
    </item>
    <item>
      <title>Tech Company Announces Product - Reuters</title>
      <link>https://news.google.com/rss/articles/CBMiYY456</link>
      <guid isPermaLink="false">CBMiYY456</guid>
      <pubDate>Wed, 19 Feb 2026 09:00:00 GMT</pubDate>
      <description>&lt;ol&gt;&lt;li&gt;cluster content&lt;/li&gt;&lt;/ol&gt;</description>
      <source url="https://www.reuters.com">Reuters</source>
    </item>
    <item>
      <title>Item Without Source Element</title>
      <link>https://news.google.com/rss/articles/CBMiZZ789</link>
      <pubDate>Wed, 19 Feb 2026 08:00:00 GMT</pubDate>
    </item>
  </channel>
</rss>`

func TestParseString(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) != 3 {
		t.Errorf("Expected 3 articles, got %d", len(articles))
	}
}

func TestParseString_ExtractsFields(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) < 2 {
		t.Fatal("Expected at least 2 articles")
	}

	tests := []struct {
		name        string
		article     Article
		wantTitle   string
		wantURL     string
		wantSource  string
		wantSrcURL  string
		wantPubAt   time.Time
	}{
		{
			name:       "first item",
			article:    articles[0],
			wantTitle:  "Breaking: Major Event Unfolds - The New York Times",
			wantURL:    "https://news.google.com/rss/articles/CBMiXX123",
			wantSource: "The New York Times",
			wantSrcURL: "https://www.nytimes.com",
			wantPubAt:  time.Date(2026, 2, 19, 10, 30, 0, 0, time.UTC),
		},
		{
			name:       "second item",
			article:    articles[1],
			wantTitle:  "Tech Company Announces Product - Reuters",
			wantURL:    "https://news.google.com/rss/articles/CBMiYY456",
			wantSource: "Reuters",
			wantSrcURL: "https://www.reuters.com",
			wantPubAt:  time.Date(2026, 2, 19, 9, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.article.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", tt.article.Title, tt.wantTitle)
			}
			if tt.article.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", tt.article.URL, tt.wantURL)
			}
			if tt.article.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", tt.article.Source, tt.wantSource)
			}
			if tt.article.SourceURL != tt.wantSrcURL {
				t.Errorf("SourceURL = %q, want %q", tt.article.SourceURL, tt.wantSrcURL)
			}
			if !tt.article.PublishedAt.Equal(tt.wantPubAt) {
				t.Errorf("PublishedAt = %v, want %v", tt.article.PublishedAt, tt.wantPubAt)
			}
		})
	}
}

func TestParseString_ItemWithoutSource(t *testing.T) {
	p := NewParser()

	articles, err := p.ParseString(sampleFeed)
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}

	if len(articles) < 3 {
		t.Fatalf("Expected at least 3 articles, got %d", len(articles))
	}

	third := articles[2]
	if third.Source != "" {
		t.Errorf("Source = %q, want empty string", third.Source)
	}
	if third.SourceURL != "" {
		t.Errorf("SourceURL = %q, want empty string", third.SourceURL)
	}
}

func TestParseString_EmptyFeed(t *testing.T) {
	p := NewParser()

	emptyFeed := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Top stories - Google News</title>
    <link>https://news.google.com</link>
    <description>Google News</description>
  </channel>
</rss>`

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

	_, err := p.ParseString("not valid xml at all")
	if err == nil {
		t.Error("Expected error for invalid feed, got nil")
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

	if p.httpClient == nil {
		t.Error("httpClient is nil")
	}
}
