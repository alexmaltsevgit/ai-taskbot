package taskbot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type IssueEditor interface {
	GetIssue(context.Context, string, int) (Issue, error)
	UpdateIssue(context.Context, string, int, string, string) error
}

var sourceTags = regexp.MustCompile(`<!-- issue-pilot-source:[a-f0-9]{64} -->`)

var issueLink = regexp.MustCompile(`https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/issues/([1-9][0-9]*)\b`)

func (s *Service) prepareEdit(ctx context.Context, r Request, policy Policy, marker, fingerprint string, discussion []ContextMessage) (Result, error) {
	links := issueLink.FindAllStringSubmatch(r.Instruction, -1)
	if len(links) != 1 {
		return question("Для редактирования укажите одну полную ссылку на GitHub issue после /edit.", ""), nil
	}
	repository := links[0][1]
	number, err := strconv.Atoi(links[0][2])
	repository, authorized := canonicalRepository(policy.Repositories, repository)
	if err != nil || !authorized {
		return Result{Kind: "denied"}, nil
	}
	editor, ok := s.Github.(IssueEditor)
	if !ok {
		return Result{}, fmt.Errorf("issue editing unavailable")
	}
	repos, err := s.Github.Repositories(ctx, []string{repository})
	if err != nil {
		return Result{}, err
	}
	_, _, wildcard := s.policyFor(r.ChatID)
	if wildcard && len(repos) == 1 && repos[0].Private && !policy.AllowPrivateInAnyChat {
		return Result{Kind: "denied"}, nil
	}
	if len(repos) != 1 {
		return Result{}, fmt.Errorf("repository unavailable")
	}
	original, err := editor.GetIssue(ctx, repository, number)
	if err != nil {
		return Result{}, err
	}
	// Do not silently truncate an existing issue and then replace its entire body.
	if len([]rune(original.Body)) > 12000 {
		return question("Существующая задача слишком длинная для безопасного редактирования этим ботом.", ""), nil
	}
	modelIssue := original
	modelIssue.Title = RedactSecrets(modelIssue.Title)
	modelIssue.Body = RedactSecrets(modelIssue.Body)
	proposal, err := s.Model.Propose(ctx, Payload{Instruction: RedactSecrets(r.Instruction), QuotedMessage: RedactSecrets(r.Source), ChatTitle: r.ChatTitle, Discussion: discussion, Repositories: repos, PreferredRepository: repository, Issues: []Issue{}, EditTarget: &modelIssue, HasUnreadAttachment: r.HasAttachment})
	if err != nil {
		return Result{}, err
	}
	if err = ValidateProposal(proposal, []string{repository}, nil); err != nil {
		return Result{}, err
	}
	if proposal.Question != "" || proposal.Confidence < .85 {
		return question(proposal.Question, "Уточните, какие изменения внести в задачу."), nil
	}
	proposal.Title = RedactSecrets(proposal.Title)
	proposal.Body = strings.TrimSpace(sourceTags.ReplaceAllString(RedactSecrets(proposal.Body), ""))
	for _, tag := range sourceTags.FindAllString(original.Body, -1) {
		if !strings.Contains(proposal.Body, tag) {
			proposal.Body += "\n\n" + tag
		}
	}
	if err = ValidateProposal(proposal, []string{repository}, nil); err != nil {
		return Result{}, err
	}
	token := make([]byte, 18)
	if _, err = rand.Read(token); err != nil {
		return Result{}, err
	}
	draft := Draft{Token: hex.EncodeToString(token), Source: marker, Fingerprint: fingerprint, State: "preview", UserID: r.UserID, ChatID: r.ChatID, Proposal: proposal, Private: repos[0].Private, Created: s.now(), EditTarget: &original}
	if err = s.Store.Save(draft); err != nil {
		return Result{}, err
	}
	return resultFromDraft(draft), nil
}
func (s *Service) confirmEdit(ctx context.Context, draft Draft) (Result, error) {
	editor, ok := s.Github.(IssueEditor)
	if !ok {
		return Result{}, fmt.Errorf("issue editing unavailable")
	}
	original := *draft.EditTarget
	current, err := editor.GetIssue(ctx, draft.Proposal.Repository, original.Number)
	if err != nil {
		return Result{}, err
	}
	if current.Title != original.Title || current.Body != original.Body {
		_, err = s.Store.Transition(draft.Token, "preview", "expired", "")
		return Result{Kind: "expired"}, err
	}
	claimed, err := s.Store.Transition(draft.Token, "preview", "creating", "")
	if err != nil {
		return Result{}, err
	}
	if !claimed {
		return Result{}, fmt.Errorf("draft changed")
	}
	p := draft.Proposal
	_ = editor.UpdateIssue(ctx, p.Repository, original.Number, p.Title, p.Body)
	// Read back even after a timeout. A matching target proves the desired outcome;
	// otherwise preserve the barrier and never retry a potentially applied PATCH.
	actual, readErr := editor.GetIssue(ctx, p.Repository, original.Number)
	state := "uncertain"
	if readErr == nil && actual.Title == p.Title && actual.Body == p.Body {
		state = "edited"
	}
	url := original.URL
	if _, saveErr := s.Store.Transition(draft.Token, "creating", state, url); saveErr != nil {
		return Result{Kind: "uncertain", URL: url}, saveErr
	}
	return Result{Kind: state, URL: url, Proposal: p}, nil
}
func (c *GithubClient) GetIssue(ctx context.Context, repository string, number int) (Issue, error) {
	var raw candidateIssue
	if err := c.request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repository, number), nil, &raw); err != nil {
		return Issue{}, err
	}
	expected := fmt.Sprintf("https://github.com/%s/issues/%d", repository, number)
	if len(raw.PullRequest) > 0 || raw.Number != number || raw.URL != expected {
		return Issue{}, fmt.Errorf("not the requested issue")
	}
	return raw.Issue, nil
}
func (c *GithubClient) UpdateIssue(ctx context.Context, repository string, number int, title, body string) error {
	var result Issue
	return c.request(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repository, number), map[string]string{"title": title, "body": body}, &result)
}
