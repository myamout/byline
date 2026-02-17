package ossinsight

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sampleResponse is a valid JSON response matching the OSSInsight API format.
const sampleResponse = `{
  "type": "sql_endpoint",
  "data": {
    "columns": [
      {"col": "repo_id", "data_type": "INT", "nullable": true},
      {"col": "repo_name", "data_type": "VARCHAR", "nullable": true}
    ],
    "rows": [
      {
        "repo_id": 1001,
        "repo_name": "octocat/hello-world",
        "primary_language": "Go",
        "description": "A test repository",
        "stars": 5000,
        "forks": 1200,
        "pull_requests": 80,
        "pushes": 400,
        "total_score": 6680,
        "contributor_logins": "alice,bob",
        "collection_names": "awesome-go"
      },
      {
        "repo_id": 1002,
        "repo_name": "org/project",
        "primary_language": "Rust",
        "description": "Another test repo",
        "stars": 3000,
        "forks": 600,
        "pull_requests": 40,
        "pushes": 200,
        "total_score": 3840,
        "contributor_logins": "charlie",
        "collection_names": ""
      }
    ],
    "result": {
      "code": 200,
      "message": "Query OK!",
      "request_id": "req-abc-123",
      "started_at": 1700000000,
      "finished_at": 1700000001,
      "latency": "1s",
      "row_count": 2,
      "limit": 30,
      "databases": ["gharchive_dev"]
    }
  }
}`

func TestGetTrendingRepos_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET request, got %s", r.Method)
		}

		if r.URL.Path != "/v1/trending/repos" {
			t.Errorf("expected path /v1/trending/repos, got %s", r.URL.Path)
		}

		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("expected Accept header application/json, got %s", accept)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	repos, err := client.GetTrendingRepos(context.Background(), TrendingReposOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}

	// Verify first repository fields.
	first := repos[0]
	if first.RepoID != 1001 {
		t.Errorf("expected RepoID 1001, got %d", first.RepoID)
	}
	if first.RepoName != "octocat/hello-world" {
		t.Errorf("expected RepoName octocat/hello-world, got %s", first.RepoName)
	}
	if first.PrimaryLanguage != "Go" {
		t.Errorf("expected PrimaryLanguage Go, got %s", first.PrimaryLanguage)
	}
	if first.Description != "A test repository" {
		t.Errorf("expected Description 'A test repository', got %s", first.Description)
	}
	if first.Stars != 5000 {
		t.Errorf("expected Stars 5000, got %d", first.Stars)
	}
	if first.Forks != 1200 {
		t.Errorf("expected Forks 1200, got %d", first.Forks)
	}
	if first.PullRequests != 80 {
		t.Errorf("expected PullRequests 80, got %d", first.PullRequests)
	}
	if first.Pushes != 400 {
		t.Errorf("expected Pushes 400, got %d", first.Pushes)
	}
	if first.TotalScore != 6680 {
		t.Errorf("expected TotalScore 6680, got %d", first.TotalScore)
	}
	if first.ContributorLogins != "alice,bob" {
		t.Errorf("expected ContributorLogins alice,bob, got %s", first.ContributorLogins)
	}
	if first.CollectionNames != "awesome-go" {
		t.Errorf("expected CollectionNames awesome-go, got %s", first.CollectionNames)
	}

	// Verify second repository.
	second := repos[1]
	if second.RepoID != 1002 {
		t.Errorf("expected RepoID 1002, got %d", second.RepoID)
	}
	if second.RepoName != "org/project" {
		t.Errorf("expected RepoName org/project, got %s", second.RepoName)
	}
}

func TestGetTrendingRepos_QueryParameters(t *testing.T) {
	tests := []struct {
		name            string
		opts            TrendingReposOptions
		expectedPeriod  string
		expectedLang    string
		expectedRawLang string // the URI-encoded form expected in the raw query
	}{
		{
			name:           "no options sends no query params",
			opts:           TrendingReposOptions{},
			expectedPeriod: "",
			expectedLang:   "",
		},
		{
			name: "period only",
			opts: TrendingReposOptions{
				Period: PeriodPastWeek,
			},
			expectedPeriod: "past_week",
			expectedLang:   "",
		},
		{
			name: "language only",
			opts: TrendingReposOptions{
				Language: "Go",
			},
			expectedPeriod: "",
			expectedLang:   "Go",
		},
		{
			name: "period and language",
			opts: TrendingReposOptions{
				Period:   PeriodPastMonth,
				Language: "JavaScript",
			},
			expectedPeriod: "past_month",
			expectedLang:   "JavaScript",
		},
		{
			name: "C++ language is URI encoded",
			opts: TrendingReposOptions{
				Period:   PeriodPast24Hours,
				Language: "C++",
			},
			expectedPeriod:  "past_24_hours",
			expectedLang:    "C++",
			expectedRawLang: "C%2B%2B",
		},
		{
			name: "C# language is URI encoded",
			opts: TrendingReposOptions{
				Language: "C#",
			},
			expectedLang:    "C#",
			expectedRawLang: "C%23",
		},
		{
			name: "past 3 months period",
			opts: TrendingReposOptions{
				Period: PeriodPast3Months,
			},
			expectedPeriod: "past_3_months",
			expectedLang:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := r.URL.Query()

				gotPeriod := query.Get("period")
				if gotPeriod != tc.expectedPeriod {
					t.Errorf("expected period %q, got %q", tc.expectedPeriod, gotPeriod)
				}

				gotLang := query.Get("language")
				if gotLang != tc.expectedLang {
					t.Errorf("expected language %q, got %q", tc.expectedLang, gotLang)
				}

				// Verify raw query encoding for special characters.
				if tc.expectedRawLang != "" {
					rawQuery := r.URL.RawQuery
					if !strings.Contains(rawQuery, "language="+tc.expectedRawLang) {
						t.Errorf("expected raw query to contain language=%s, got raw query %q",
							tc.expectedRawLang, rawQuery)
					}
				}

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(sampleResponse))
			}))
			defer server.Close()

			client := &Client{
				httpClient: server.Client(),
				baseURL:    server.URL,
			}

			_, err := client.GetTrendingRepos(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestGetTrendingRepos_NonOKStatus(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "500 internal server error",
			statusCode: http.StatusInternalServerError,
			body:       `{"error": "internal server error"}`,
		},
		{
			name:       "404 not found",
			statusCode: http.StatusNotFound,
			body:       `{"error": "not found"}`,
		},
		{
			name:       "429 rate limited",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error": "rate limit exceeded"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := &Client{
				httpClient: server.Client(),
				baseURL:    server.URL,
			}

			repos, err := client.GetTrendingRepos(context.Background(), TrendingReposOptions{})
			if err == nil {
				t.Fatal("expected error for non-200 status, got nil")
			}

			if repos != nil {
				t.Errorf("expected nil repos on error, got %v", repos)
			}

			// Verify the error message includes the status code.
			expectedFragment := fmt.Sprintf("unexpected status code %d", tc.statusCode)
			if !strings.Contains(err.Error(), expectedFragment) {
				t.Errorf("expected error to contain %q, got %q", expectedFragment, err.Error())
			}
		})
	}
}

func TestGetTrendingRepos_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{this is not valid json`))
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	repos, err := client.GetTrendingRepos(context.Background(), TrendingReposOptions{})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}

	if repos != nil {
		t.Errorf("expected nil repos on error, got %v", repos)
	}

	if !strings.Contains(err.Error(), "decoding response JSON") {
		t.Errorf("expected error to mention JSON decoding, got %q", err.Error())
	}
}

func TestGetTrendingRepos_EmptyRows(t *testing.T) {
	emptyResponse := `{
		"type": "sql_endpoint",
		"data": {
			"columns": [],
			"rows": [],
			"result": {
				"code": 200,
				"message": "Query OK!",
				"request_id": "req-empty",
				"started_at": 1700000000,
				"finished_at": 1700000001,
				"latency": "0s",
				"row_count": 0,
				"limit": 30,
				"databases": ["gharchive_dev"]
			}
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(emptyResponse))
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	repos, err := client.GetTrendingRepos(context.Background(), TrendingReposOptions{
		Period:   PeriodPastWeek,
		Language: "Haskell",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(repos))
	}
}

func TestGetTrendingRepos_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow server that never responds in time.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(sampleResponse))
	}))
	defer server.Close()

	client := &Client{
		httpClient: server.Client(),
		baseURL:    server.URL,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := client.GetTrendingRepos(ctx, TrendingReposOptions{})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestNewClient_Defaults(t *testing.T) {
	client := NewClient()

	if client.baseURL != defaultBaseURL {
		t.Errorf("expected baseURL %q, got %q", defaultBaseURL, client.baseURL)
	}

	if client.httpClient == nil {
		t.Fatal("expected non-nil httpClient")
	}

	if client.httpClient.Timeout != defaultTimeout {
		t.Errorf("expected timeout %v, got %v", defaultTimeout, client.httpClient.Timeout)
	}
}

func TestPeriodConstants(t *testing.T) {
	tests := []struct {
		period   Period
		expected string
	}{
		{PeriodPast24Hours, "past_24_hours"},
		{PeriodPastWeek, "past_week"},
		{PeriodPastMonth, "past_month"},
		{PeriodPast3Months, "past_3_months"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			if string(tc.period) != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, string(tc.period))
			}
		})
	}
}
