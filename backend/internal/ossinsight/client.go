// Package ossinsight provides an HTTP client for the OSSInsight API,
// enabling retrieval of trending open-source repository data.
package ossinsight

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// defaultBaseURL is the base URL for the OSSInsight API.
	defaultBaseURL = "https://api.ossinsight.io"

	// defaultTimeout is the default HTTP client timeout.
	defaultTimeout = 10 * time.Second
)

// Period represents a time range filter for trending repositories.
type Period string

const (
	// PeriodPast24Hours filters for repositories trending in the last 24 hours.
	PeriodPast24Hours Period = "past_24_hours"

	// PeriodPastWeek filters for repositories trending in the past week.
	PeriodPastWeek Period = "past_week"

	// PeriodPastMonth filters for repositories trending in the past month.
	PeriodPastMonth Period = "past_month"

	// PeriodPast3Months filters for repositories trending in the past 3 months.
	PeriodPast3Months Period = "past_3_months"
)

// TrendingReposOptions configures the request to the trending repos endpoint.
type TrendingReposOptions struct {
	// Period is the time range filter. If empty, the API defaults to "past_24_hours".
	Period Period

	// Language filters results by programming language (e.g. "Go", "C++", "JavaScript").
	// If empty, the API defaults to "All" languages.
	Language string
}

// Repository represents a single trending repository returned by the OSSInsight API.
type Repository struct {
	// RepoID is the unique identifier of the repository.
	RepoID int `json:"repo_id"`

	// RepoName is the full name of the repository (e.g. "owner/repo").
	RepoName string `json:"repo_name"`

	// PrimaryLanguage is the primary programming language of the repository.
	PrimaryLanguage string `json:"primary_language"`

	// Description is the repository description.
	Description string `json:"description"`

	// Stars is the total star count.
	Stars int `json:"stars"`

	// Forks is the total fork count.
	Forks int `json:"forks"`

	// PullRequests is the number of pull requests in the trending period.
	PullRequests int `json:"pull_requests"`

	// Pushes is the number of pushes in the trending period.
	Pushes int `json:"pushes"`

	// TotalScore is the composite trending score.
	TotalScore int `json:"total_score"`

	// ContributorLogins is a comma-separated list of contributor usernames.
	ContributorLogins string `json:"contributor_logins"`

	// CollectionNames is a comma-separated list of collection names.
	CollectionNames string `json:"collection_names"`
}

// trendingReposResponse is the top-level JSON response from the trending repos endpoint.
type trendingReposResponse struct {
	Type string               `json:"type"`
	Data trendingReposData    `json:"data"`
}

// trendingReposData holds the data payload of the trending repos response.
type trendingReposData struct {
	Columns []column           `json:"columns"`
	Rows    []Repository       `json:"rows"`
	Result  trendingReposResult `json:"result"`
}

// column describes a single column in the API response metadata.
type column struct {
	Col      string `json:"col"`
	DataType string `json:"data_type"`
	Nullable bool   `json:"nullable"`
}

// trendingReposResult holds metadata about the query execution.
type trendingReposResult struct {
	Code       int    `json:"code"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
	Latency    string `json:"latency"`
	RowCount   int    `json:"row_count"`
	Limit      int    `json:"limit"`
	Databases  []string `json:"databases"`
}

// Client is an HTTP client for the OSSInsight API.
type Client struct {
	// httpClient is the underlying HTTP client used for requests.
	httpClient *http.Client

	// baseURL is the base URL of the OSSInsight API.
	baseURL string
}

// NewClient creates a new OSSInsight API client with sensible defaults.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		baseURL: defaultBaseURL,
	}
}

// GetTrendingRepos retrieves a list of trending repositories from the OSSInsight API.
// The results can be filtered by time period and programming language using opts.
func (c *Client) GetTrendingRepos(ctx context.Context, opts TrendingReposOptions) ([]Repository, error) {
	reqURL, err := c.buildTrendingReposURL(opts)
	if err != nil {
		return nil, fmt.Errorf("building request URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing HTTP request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var trendingResp trendingReposResponse
	if err := json.Unmarshal(body, &trendingResp); err != nil {
		return nil, fmt.Errorf("decoding response JSON: %w", err)
	}

	return trendingResp.Data.Rows, nil
}

// buildTrendingReposURL constructs the full URL for the trending repos endpoint,
// including any query parameters from the provided options.
func (c *Client) buildTrendingReposURL(opts TrendingReposOptions) (string, error) {
	u, err := url.Parse(c.baseURL + "/v1/trending/repos")
	if err != nil {
		return "", fmt.Errorf("parsing base URL: %w", err)
	}

	q := u.Query()

	if opts.Period != "" {
		q.Set("period", string(opts.Period))
	}

	if opts.Language != "" {
		q.Set("language", opts.Language)
	}

	u.RawQuery = q.Encode()

	return u.String(), nil
}
