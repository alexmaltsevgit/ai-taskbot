package taskbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitHubSearchCannotInjectQualifiers(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/issues" {
			query = r.URL.Query().Get("q")
			w.Write([]byte(`{"items":[],"incomplete_results":false}`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	client := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), Token: func(context.Context) (string, error) { return "fake", nil }}
	_, err := client.Candidates(context.Background(), "acme/paint", "bug repo:evil/private is:pr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "repo:acme/paint") || strings.Contains(query, "repo:evil") {
		t.Fatal(query)
	}
}
func TestGitHubNullIssueBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/issues" {
			w.Write([]byte(`{"items":[{"number":1,"title":"Bug","body":null,"html_url":"https://github.com/a/b/issues/1"}],"incomplete_results":false}`))
		} else {
			w.Write([]byte(`[]`))
		}
	}))
	defer server.Close()
	client := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), Token: func(context.Context) (string, error) { return "fake", nil }}
	issues, err := client.Candidates(context.Background(), "acme/paint", "Bug")
	if err != nil || len(issues) != 1 || issues[0].Body != "" {
		t.Fatal(issues, err)
	}
}
func TestIncompleteSearchFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[],"incomplete_results":true}`))
	}))
	defer server.Close()
	client := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), Token: func(context.Context) (string, error) { return "fake", nil }}
	if _, err := client.Candidates(context.Background(), "acme/paint", "Bug"); err == nil {
		t.Fatal("incomplete accepted")
	}
}
