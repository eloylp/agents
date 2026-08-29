package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type githubStatusError struct {
	StatusCode int
	Body       string
}

func (e *githubStatusError) Error() string {
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Body)
}

type catalogGitHubClient interface {
	BranchHead(ctx context.Context, cfg catalogGitHubConfig) (string, error)
	ReadFile(ctx context.Context, cfg catalogGitHubConfig, ref string) ([]byte, string, error)
	WriteFile(ctx context.Context, cfg catalogGitHubConfig, message string, content []byte, expectedBlobSHA string) (string, error)
}

type catalogGitHubConfig struct {
	Repo        string
	Branch      string
	CatalogPath string
	Token       string
}

type githubCatalogClient struct {
	httpClient *http.Client
	baseURL    string
}

func newGitHubCatalogClient() *githubCatalogClient {
	return &githubCatalogClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    "https://api.github.com",
	}
}

func (c *githubCatalogClient) BranchHead(ctx context.Context, cfg catalogGitHubConfig) (string, error) {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.do(ctx, http.MethodGet, cfg, "git/ref/heads/"+url.PathEscape(cfg.Branch), nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Object.SHA) == "" {
		return "", fmt.Errorf("github catalog: branch %q returned empty head sha", cfg.Branch)
	}
	return out.Object.SHA, nil
}

func (c *githubCatalogClient) ReadFile(ctx context.Context, cfg catalogGitHubConfig, ref string) ([]byte, string, error) {
	var out struct {
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	path := "contents/" + strings.TrimLeft(cfg.CatalogPath, "/") + "?ref=" + url.QueryEscape(ref)
	if err := c.do(ctx, http.MethodGet, cfg, path, nil, &out); err != nil {
		return nil, "", err
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return nil, "", fmt.Errorf("github catalog: decode %s: %w", cfg.CatalogPath, err)
	}
	return content, out.SHA, nil
}

func (c *githubCatalogClient) WriteFile(ctx context.Context, cfg catalogGitHubConfig, message string, content []byte, expectedBlobSHA string) (string, error) {
	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  cfg.Branch,
	}
	if expectedBlobSHA != "" {
		body["sha"] = expectedBlobSHA
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	path := "contents/" + strings.TrimLeft(cfg.CatalogPath, "/")
	if err := c.do(ctx, http.MethodPut, cfg, path, body, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Commit.SHA) == "" {
		return "", fmt.Errorf("github catalog: write %s returned empty commit sha", cfg.CatalogPath)
	}
	return out.Commit.SHA, nil
}

func (c *githubCatalogClient) do(ctx context.Context, method string, cfg catalogGitHubConfig, path string, body any, out any) error {
	owner, repo, ok := strings.Cut(cfg.Repo, "/")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" {
		return fmt.Errorf("github catalog: repo must be owner/name")
	}
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github catalog: encode request: %w", err)
		}
		r = bytes.NewReader(b)
	}
	u := strings.TrimRight(c.baseURL, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/" + path
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return fmt.Errorf("github catalog: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("github catalog: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github catalog: %s %s: %w", method, path, &githubStatusError{
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(b)),
		})
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("github catalog: decode response: %w", err)
	}
	return nil
}

func isGitHubNotFound(err error) bool {
	var status *githubStatusError
	return errors.As(err, &status) && status.StatusCode == http.StatusNotFound
}
