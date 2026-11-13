package taskbot

import (
	"context"
	"testing"
)

func TestWildcardPolicyValidationAndExactDeny(t *testing.T) {
	s := Service{Config: Config{"*": {Users: []int64{1}, Repositories: []string{"*"}}, "10": {Users: []int64{2}, Repositories: []string{"acme/paint"}}}}
	if err := ValidateConfig(s.Config); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Allowed(1, 99); !ok {
		t.Fatal("wildcard user denied")
	}
	if _, ok := s.Allowed(1, 10); ok {
		t.Fatal("exact deny fell back")
	}
	if _, ok := s.Allowed(3, 99); ok {
		t.Fatal("unknown user")
	}
	for _, policy := range []Policy{
		{Repositories: []string{"*"}},
		{Users: []int64{1}, Repositories: []string{"*"}, HistoryEnabled: true},
		{Users: []int64{1}, Repositories: []string{"*", "acme/paint"}},
	} {
		if ValidateConfig(Config{"*": policy}) == nil {
			t.Fatal("unsafe policy accepted", policy)
		}
	}
}

type policyGithub struct {
	fakeGithub
	repos           []Repository
	reads, searches int
}

func (g *policyGithub) Repositories(context.Context, []string) ([]Repository, error) {
	return g.repos, nil
}
func (g *policyGithub) Candidates(ctx context.Context, r, q string) ([]Issue, error) {
	g.searches++
	return g.fakeGithub.Candidates(ctx, r, q)
}
func (g *policyGithub) GetIssue(context.Context, string, int) (Issue, error) {
	g.reads++
	return Issue{Number: 9, Title: "Private", Body: "Sensitive", URL: "https://github.com/acme/paint/issues/9"}, nil
}
func (g *policyGithub) UpdateIssue(context.Context, string, int, string, string) error { return nil }
func TestWildcardPrivateDisclosureOptIn(t *testing.T) {
	for _, allow := range []bool{false, true} {
		s, _, r := fixture(t)
		s.Config = Config{"*": {Users: []int64{r.UserID}, Repositories: []string{"*"}, AllowPrivateInAnyChat: allow}}
		g := &policyGithub{repos: []Repository{{FullName: "acme/paint", Private: true}}}
		s.Github = g
		m := &capturingModel{}
		s.Model = m
		result, err := s.Prepare(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if !allow && (result.Kind != "denied" || g.searches != 0 || len(m.payloads) != 0) {
			t.Fatal(result, g.searches, m.payloads)
		}
		if allow && result.Kind != "preview" {
			t.Fatal(result)
		}
	}
}
func TestPrivateEditAndStoredResultsRechecked(t *testing.T) {
	s, _, r := fixture(t)
	g := &policyGithub{repos: []Repository{{FullName: "acme/paint", Private: true}}}
	s.Github = g
	preview, err := s.Prepare(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.Config = Config{"*": {Users: []int64{r.UserID}, Repositories: []string{"*"}}}
	for _, action := range []func() (Result, error){
		func() (Result, error) { return s.Prepare(context.Background(), r) },
		func() (Result, error) { return s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID) },
		func() (Result, error) {
			return s.CancelWithContext(context.Background(), preview.Token, r.UserID, r.ChatID)
		},
	} {
		result, err := action()
		if err != nil || result.Kind != "denied" || result.URL != "" || result.Proposal.Body != "" {
			t.Fatal(result, err)
		}
	}
	r.Edit = true
	r.Instruction = "/edit https://github.com/acme/paint/issues/9 change"
	result, err := s.Prepare(context.Background(), r)
	if err != nil || result.Kind != "denied" || g.reads != 0 {
		t.Fatal(result, err, g.reads)
	}
	// Even a previously completed cached result must not bypass a narrowed policy.
	_, _ = s.Store.Transition(preview.Token, "preview", "created", "https://github.com/acme/paint/issues/9")
	result, err = s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
	if err != nil || result.Kind != "denied" || result.URL != "" {
		t.Fatal(result, err)
	}
}
func TestExactDenyAppliesToCallbacks(t *testing.T) {
	s, _, r := fixture(t)
	preview, err := s.Prepare(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	s.Config = Config{"*": {Users: []int64{r.UserID}, Repositories: []string{"*"}, AllowPrivateInAnyChat: true}, "-1001": {Users: []int64{99}, Repositories: []string{"acme/paint"}}}
	result, err := s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
	if err != nil || result.Kind != "denied" {
		t.Fatal(result, err)
	}
	result, err = s.CancelWithContext(context.Background(), preview.Token, r.UserID, r.ChatID)
	if err != nil || result.Kind != "denied" {
		t.Fatal(result, err)
	}
}

func TestRepositoryCanonicalNamesAndDotRepository(t *testing.T) {
	config := Config{"*": {Users: []int64{1}, Repositories: []string{"acme/.github"}, DefaultRepository: "ACME/.GitHub"}}
	if err := ValidateConfig(config); err != nil {
		t.Fatal(err)
	}
	s := Service{Config: config, Github: &policyGithub{repos: []Repository{{FullName: "Acme/.github"}}}}
	policy, repos, allowed, err := s.resolvePolicy(context.Background(), 1, 10)
	if err != nil || !allowed || len(repos) != 1 || policy.DefaultRepository != "Acme/.github" {
		t.Fatal(policy, repos, allowed, err)
	}
	draft := Draft{Proposal: Proposal{Repository: "acme/.GITHUB"}}
	if !s.draftVisible(draft, policy, repos, 10) {
		t.Fatal("canonical old result denied")
	}
}
