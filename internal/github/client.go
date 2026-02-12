package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
)

const (
	defaultUserAgent = "agents-daemon/1.0"
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     zerolog.Logger
}

type Label struct {
	Name string `json:"name"`
}

type Issue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	UpdatedAt   time.Time `json:"updated_at"`
	Labels      []Label   `json:"labels"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
	Draft     bool      `json:"draft"`
	Labels    []Label   `json:"labels"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type Comment struct {
	Body      string    `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PullFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"`
}

func NewClient(cfg config.GitHubConfig, logger zerolog.Logger) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(cfg.APIBaseURL, "/"),
		token:      cfg.Token,
		httpClient: &http.Client{Timeout: 20 * time.Second},
		logger:     logger.With().Str("component", "github_client").Logger(),
	}
}

func (c *Client) ListIssues(ctx context.Context, repo string, since *time.Time, perPage, maxItems int) ([]Issue, error) {
	path := fmt.Sprintf("/repos/%s/issues", repo)
	params := map[string]string{
		"state":     "all",
		"sort":      "updated",
		"direction": "desc",
		"per_page":  strconv.Itoa(perPage),
	}
	if since != nil {
		params["since"] = since.UTC().Format(time.RFC3339)
	}
	return c.listIssues(ctx, path, params, since, perPage, maxItems)
}

func (c *Client) ListPullRequests(ctx context.Context, repo string, since *time.Time, perPage, maxItems int) ([]PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/pulls", repo)
	params := map[string]string{
		"state":     "all",
		"sort":      "updated",
		"direction": "desc",
		"per_page":  strconv.Itoa(perPage),
	}
	return c.listPulls(ctx, path, params, since, perPage, maxItems)
}

func (c *Client) ListIssueComments(ctx context.Context, repo string, number int, limit int) ([]Comment, error) {
	if limit <= 0 {
		return nil, nil
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)
	params := map[string]string{
		"sort":      "created",
		"direction": "desc",
		"per_page":  strconv.Itoa(limit),
	}
	var comments []Comment
	if err := c.get(ctx, path, params, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

func (c *Client) ListPullRequestFiles(ctx context.Context, repo string, number int, limit int) ([]PullFile, error) {
	if limit <= 0 {
		return nil, nil
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/files", repo, number)
	params := map[string]string{
		"per_page": strconv.Itoa(limit),
	}
	var files []PullFile
	if err := c.get(ctx, path, params, &files); err != nil {
		return nil, err
	}
	return files, nil
}

func (c *Client) listIssues(ctx context.Context, path string, params map[string]string, since *time.Time, perPage, maxItems int) ([]Issue, error) {
	var results []Issue
	for page := 1; len(results) < maxItems; page++ {
		params["page"] = strconv.Itoa(page)
		var batch []Issue
		if err := c.get(ctx, path, params, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, issue := range batch {
			if issue.PullRequest != nil {
				continue
			}
			if since != nil && issue.UpdatedAt.Before(*since) {
				return results, nil
			}
			results = append(results, issue)
			if len(results) >= maxItems {
				return results, nil
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return results, nil
}

func (c *Client) listPulls(ctx context.Context, path string, params map[string]string, since *time.Time, perPage, maxItems int) ([]PullRequest, error) {
	var results []PullRequest
	for page := 1; len(results) < maxItems; page++ {
		params["page"] = strconv.Itoa(page)
		var batch []PullRequest
		if err := c.get(ctx, path, params, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, pr := range batch {
			if since != nil && pr.UpdatedAt.Before(*since) {
				return results, nil
			}
			results = append(results, pr)
			if len(results) >= maxItems {
				return results, nil
			}
		}
		if len(batch) < perPage {
			break
		}
	}
	return results, nil
}

func (c *Client) get(ctx context.Context, path string, params map[string]string, target any) error {
	reqURL, err := url.Parse(c.baseURL + path)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	query := reqURL.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	reqURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request github: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 || (resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0") {
		retryAfter := parseRateLimitReset(resp.Header)
		c.logger.Warn().Str("path", path).Dur("retry_after", retryAfter).Msg("github rate limit hit")
		return &RateLimitError{RetryAfter: retryAfter}
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github api error: %s", resp.Status)
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode github response: %w", err)
	}
	return nil
}

func HasLabel(labels []Label, target string) bool {
	if target == "" {
		return true
	}
	for _, label := range labels {
		if strings.EqualFold(label.Name, target) {
			return true
		}
	}
	return false
}

// RateLimitError is returned when the GitHub API rate limit is exceeded.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github rate limit exceeded, retry after %s", e.RetryAfter)
}

func parseRateLimitReset(header http.Header) time.Duration {
	if ra := header.Get("Retry-After"); ra != "" {
		if seconds, err := strconv.Atoi(ra); err == nil {
			return time.Duration(seconds) * time.Second
		}
	}
	if reset := header.Get("X-RateLimit-Reset"); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if d := time.Until(time.Unix(epoch, 0)); d > 0 {
				return d
			}
		}
	}
	return 60 * time.Second
}
