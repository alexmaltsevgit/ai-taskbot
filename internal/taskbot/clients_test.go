package taskbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestRepositoryDiscoveryPaginationAndCredentialEndpoints(t *testing.T) {
	for _, installation := range []bool{false, true} {
		t.Run(map[bool]string{false: "PAT", true: "App"}[installation], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				expected := "/user/repos"
				if installation {
					expected = "/installation/repositories"
				}
				if r.URL.Path != expected || r.URL.Query().Get("per_page") != "100" {
					t.Errorf("unexpected request %s", r.URL)
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				repos := []map[string]any{}
				if page == 1 {
					for i := 0; i < 100; i++ {
						repos = append(repos, map[string]any{"full_name": fmt.Sprintf("team/r%03d", 99-i), "has_issues": true, "private": true, "description": "Project"})
					}
				}
				if page == 2 {
					repos = append(repos, map[string]any{"full_name": "a/.github", "has_issues": true, "private": false})
				}
				if installation {
					json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "repositories": repos})
				} else {
					json.NewEncoder(w).Encode(repos)
				}
			}))
			defer server.Close()
			c := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), InstallationToken: installation, Token: func(context.Context) (string, error) { return "test", nil }}
			repos, err := c.Repositories(context.Background(), []string{"*"})
			if err != nil || len(repos) != 101 || repos[0].FullName != "a/.github" || !repos[1].Private || repos[1].Description != "Project" || calls != 2 {
				t.Fatalf("repos=%d calls=%d err=%v", len(repos), calls, err)
			}
		})
	}
}
func TestRepositoryDiscoveryFailsClosed(t *testing.T) {
	for _, scenario := range []string{"empty", "forbidden", "bad-name", "too-many", "missing-list", "mixed-wildcard", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch scenario {
				case "forbidden":
					http.Error(w, "sensitive upstream error", 403)
				case "missing-list":
					w.Write([]byte(`{}`))
				case "bad-name":
					w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"../bad","has_issues":true}]}`))
				case "too-many":
					w.Write([]byte(`{"total_count":501,"repositories":[]}`))
				case "unavailable":
					w.Write([]byte(`{"total_count":1,"repositories":[{"full_name":"team/project","has_issues":false}]}`))
				default:
					w.Write([]byte(`{"total_count":0,"repositories":[]}`))
				}
			}))
			defer server.Close()
			c := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), InstallationToken: true, Token: func(context.Context) (string, error) { return "secret-test", nil }}
			names := []string{"*"}
			if scenario == "mixed-wildcard" {
				names = append(names, "team/project")
			}
			repos, err := c.Repositories(context.Background(), names)
			if err == nil || repos != nil || strings.Contains(err.Error(), "secret-test") || strings.Contains(err.Error(), "sensitive") {
				t.Fatal(repos, err)
			}
			if scenario == "mixed-wildcard" && calls != 0 {
				t.Fatal("invalid config reached network")
			}
		})
	}
}

func TestPATRepositoryDiscoveryLimitAndPartialFailure(t *testing.T) {
	for _, scenario := range []string{"exact-cap", "overflow", "page-error"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				if scenario == "page-error" && page == 2 {
					http.Error(w, "unavailable", 502)
					return
				}
				count := 100
				if page == 6 {
					count = 0
					if scenario == "overflow" {
						count = 1
					}
				}
				repos := []map[string]any{}
				for i := 0; i < count; i++ {
					repos = append(repos, map[string]any{"full_name": fmt.Sprintf("team/repo%d", (page-1)*100+i), "has_issues": true})
				}
				json.NewEncoder(w).Encode(repos)
			}))
			defer server.Close()
			c := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), Token: func(context.Context) (string, error) { return "fake", nil }}
			repos, err := c.Repositories(context.Background(), []string{"*"})
			if scenario == "exact-cap" {
				if err != nil || len(repos) != 500 || calls != 6 {
					t.Fatal(len(repos), calls, err)
				}
			} else {
				if err == nil || repos != nil {
					t.Fatal("partial catalog returned", len(repos), err)
				}
			}
		})
	}
}
func TestAppRepositoryDiscoveryIncompleteAndFiltered(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprint(incomplete), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := 4
				if incomplete {
					count = 5
				}
				json.NewEncoder(w).Encode(map[string]any{"total_count": count, "repositories": []map[string]any{
					{"full_name": "team/active", "has_issues": true, "private": true},
					{"full_name": "team/archived", "has_issues": true, "archived": true},
					{"full_name": "team/disabled", "has_issues": true, "disabled": true},
					{"full_name": "team/noissues", "has_issues": false},
				}})
			}))
			defer server.Close()
			c := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), InstallationToken: true, Token: func(context.Context) (string, error) { return "fake", nil }}
			repos, err := c.Repositories(context.Background(), []string{"*"})
			if incomplete {
				if err == nil || repos != nil {
					t.Fatal(repos, err)
				}
			} else if err != nil || len(repos) != 1 || repos[0].FullName != "team/active" {
				t.Fatal(repos, err)
			}
		})
	}
}
func TestExplicitRepositoryNamesValidatedBeforeNetwork(t *testing.T) {
	called := false
	c := &GithubClient{Token: func(context.Context) (string, error) { called = true; return "", nil }}
	if _, err := c.Repositories(context.Background(), []string{"team/project", "../escape"}); err == nil || called {
		t.Fatal(err, called)
	}
}

func TestRepositoryDiscoveryRejectsDuplicateNames(t *testing.T) {
	for _, installation := range []bool{false, true} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("app=%v/changed=%v", installation, changed), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					repos := []map[string]any{}
					if page == 1 {
						for i := 0; i < 100; i++ {
							repos = append(repos, map[string]any{"full_name": fmt.Sprintf("team/r%d", i), "has_issues": false})
						}
					}
					if page == 2 {
						repos = append(repos, map[string]any{"full_name": "TEAM/R0", "has_issues": changed})
					}
					if installation {
						json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "repositories": repos})
					} else {
						json.NewEncoder(w).Encode(repos)
					}
				}))
				defer server.Close()
				c := &GithubClient{BaseURL: server.URL, HTTP: server.Client(), InstallationToken: installation, Token: func(context.Context) (string, error) { return "fake", nil }}
				repos, err := c.Repositories(context.Background(), []string{"*"})
				if err == nil || repos != nil || !strings.Contains(err.Error(), "duplicates") {
					t.Fatal(repos, err)
				}
			})
		}
	}
}
