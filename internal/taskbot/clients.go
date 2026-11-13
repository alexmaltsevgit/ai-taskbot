package taskbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func requestJSON(ctx context.Context, client *http.Client, method, address string, input, output any, headers map[string]string) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return fmt.Errorf("invalid request")
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("HTTP transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4_000_001))
	if err != nil {
		return fmt.Errorf("response read failed")
	}
	if len(raw) > 4_000_000 {
		return fmt.Errorf("response too large")
	}
	if err = json.Unmarshal(raw, output); err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	return nil
}
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type GithubClient struct {
	BaseURL string
	HTTP    *http.Client
	Token   func(context.Context) (string, error)
}

func (c *GithubClient) request(ctx context.Context, method, path string, input, output any) error {
	token, err := c.Token(ctx)
	if err != nil {
		return fmt.Errorf("GitHub authentication unavailable")
	}
	return requestJSON(ctx, c.HTTP, method, c.BaseURL+path, input, output, map[string]string{"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28", "User-Agent": "ai-taskbot"})
}
func (c *GithubClient) Repositories(ctx context.Context, names []string) ([]Repository, error) {
	repos := make([]Repository, 0, len(names))
	for _, name := range names {
		var raw struct {
			Repository
			Archived  bool `json:"archived"`
			HasIssues bool `json:"has_issues"`
		}
		if err := c.request(ctx, http.MethodGet, "/repos/"+name, nil, &raw); err != nil {
			return nil, err
		}
		if raw.Archived || !raw.HasIssues {
			return nil, fmt.Errorf("repository unavailable for issues")
		}
		repos = append(repos, raw.Repository)
	}
	return repos, nil
}

var searchWords = regexp.MustCompile(`[\pL\pN]{3,}`)

type candidateIssue struct {
	Issue
	PullRequest json.RawMessage `json:"pull_request"`
}

func (c *GithubClient) Candidates(ctx context.Context, repository, text string) ([]Issue, error) {
	terms := []string{}
	for _, word := range searchWords.FindAllString(text, -1) {
		switch strings.ToLower(word) {
		case "repo", "is", "https":
			continue
		}
		terms = append(terms, `"`+word+`"`)
		if len(terms) == 3 {
			break
		}
	}
	query := "repo:" + repository + " is:issue " + strings.Join(terms, " ")
	var found struct {
		Items      []candidateIssue `json:"items"`
		Incomplete bool             `json:"incomplete_results"`
	}
	if err := c.request(ctx, http.MethodGet, "/search/issues?"+url.Values{"q": {query}, "per_page": {"50"}}.Encode(), nil, &found); err != nil {
		return nil, err
	}
	if found.Incomplete {
		return nil, fmt.Errorf("incomplete duplicate search")
	}
	var recent []candidateIssue
	if err := c.request(ctx, http.MethodGet, "/repos/"+repository+"/issues?state=all&sort=updated&direction=desc&per_page=100", nil, &recent); err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	issues := []Issue{}
	for _, item := range append(found.Items, recent...) {
		if len(item.PullRequest) > 0 || seen[item.Number] {
			continue
		}
		seen[item.Number] = true
		issues = append(issues, item.Issue)
	}
	return issues, nil
}
func (c *GithubClient) Create(ctx context.Context, repository, title, body string) (string, error) {
	var result struct {
		URL string `json:"html_url"`
	}
	if err := c.request(ctx, http.MethodPost, "/repos/"+repository+"/issues", map[string]string{"title": title, "body": body}, &result); err != nil {
		return "", err
	}
	if !strings.HasPrefix(result.URL, "https://github.com/"+repository+"/issues/") {
		return "", fmt.Errorf("create response missing issue URL")
	}
	return result.URL, nil
}

type TelegramClient struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *TelegramClient) Call(ctx context.Context, method string, input, output any) error {
	var response struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := requestJSON(ctx, c.HTTP, http.MethodPost, c.BaseURL+method, input, &response, nil); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Telegram request rejected")
	}
	if output != nil {
		if err := json.Unmarshal(response.Result, output); err != nil {
			return fmt.Errorf("invalid Telegram result")
		}
	}
	return nil
}

type ModelClient struct {
	Endpoint, Token string
	HTTP            *http.Client
}

func (c *ModelClient) Propose(ctx context.Context, payload Payload) (Proposal, error) {
	var result Proposal
	err := requestJSON(ctx, c.HTTP, http.MethodPost, c.Endpoint+"/propose", payload, &result, map[string]string{"Authorization": "Bearer " + c.Token})
	return result, err
}
