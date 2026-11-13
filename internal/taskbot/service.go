package taskbot

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Github interface {
	Repositories(context.Context, []string) ([]Repository, error)
	Candidates(context.Context, string, string) ([]Issue, error)
	Create(context.Context, string, string, string) (string, error)
}
type Model interface {
	Propose(context.Context, Payload) (Proposal, error)
}
type Service struct {
	Store           *Store
	Github          Github
	Model           Model
	Config          Config
	Now             func() time.Time
	mu              sync.Mutex
	historyMu       sync.Mutex
	history         []historyEntry
	historySettings map[historyScope]historySetting
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
func (s *Service) Allowed(userID, chatID int64) (Policy, bool) {
	p, ok, _ := s.policyFor(chatID)
	return p, ok && slices.Contains(p.Users, userID)
}
func resultFromDraft(d Draft) Result {
	state := d.State
	if state == "creating" {
		state = "uncertain"
	}
	url := d.URL
	editNumber := 0
	if d.EditTarget != nil {
		editNumber = d.EditTarget.Number
		if url == "" {
			url = d.EditTarget.URL
		}
	}
	return Result{EditNumber: editNumber, Kind: state, Token: d.Token, URL: url, Proposal: d.Proposal, Private: d.Private}
}

func (s *Service) Prepare(ctx context.Context, r Request) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, repos, allowed, err := s.resolvePolicy(ctx, r.UserID, r.ChatID)
	if err != nil {
		return Result{}, err
	}
	if !allowed {
		return Result{Kind: "denied"}, nil
	}
	sourceID := r.SourceID
	if r.Edit {
		sourceID = r.MessageID
	}
	marker := SourceMarker(r.ChatID, sourceID)
	discussion := s.Context(r)
	contextJSON, _ := json.Marshal(discussion)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(r.Instruction+"\x00"+r.Source+string(contextJSON))))
	if existing, ok := s.Store.BySource(marker); ok {
		if !s.draftVisible(existing, policy, repos, r.ChatID) {
			return Result{Kind: "denied"}, nil
		}
		expired := existing.State == "preview" && s.now().Sub(existing.Created) > 24*time.Hour
		changed := existing.State == "preview" && existing.UserID == r.UserID && existing.Fingerprint != fingerprint
		if expired || changed || existing.State == "cancelled" || existing.State == "expired" {
			if err := s.Store.RemoveReplaceable(existing.Token); err != nil {
				return Result{}, err
			}
		} else {
			return resultFromDraft(existing), nil
		}
	}
	if strings.TrimSpace(r.Source) == "" {
		return Result{Kind: "question", Text: "Добавьте текстовое описание: вложения пока не анализируются."}, nil
	}
	if r.Edit {
		return s.prepareEdit(ctx, r, policy, marker, fingerprint, discussion)
	}
	explicit, err := ExplicitRepository(r.Instruction, policy.Repositories)
	if err != nil {
		return Result{}, err
	}
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.FullName)
	}
	preferred := explicit
	if preferred == "" {
		preferred = policy.DefaultRepository
	}
	payload := Payload{Discussion: discussion, Instruction: RedactSecrets(r.Instruction), QuotedMessage: RedactSecrets(r.Source), ChatTitle: r.ChatTitle, Repositories: repos, PreferredRepository: preferred, HasUnreadAttachment: r.HasAttachment, Issues: []Issue{}}
	routing, err := s.Model.Propose(ctx, payload)
	if err != nil {
		return Result{}, err
	}
	if err = ValidateProposal(routing, names, nil); err != nil {
		return Result{}, err
	}
	if explicit != "" && routing.Repository != explicit {
		return Result{}, fmt.Errorf("model ignored explicit repository")
	}
	if routing.Question != "" || routing.Confidence < .85 {
		return question(routing.Question, "Укажите проект в формате owner/repo и повторите команду."), nil
	}
	candidates, err := s.Github.Candidates(ctx, routing.Repository, routing.Title)
	if err != nil {
		return Result{}, err
	}
	for _, issue := range candidates {
		if strings.Contains(issue.Body, marker) {
			return Result{Kind: "duplicate", URL: issue.URL, Proposal: Proposal{Title: issue.Title, Body: issue.Body}}, nil
		}
	}
	selected := Repository{}
	for _, repo := range repos {
		if repo.FullName == routing.Repository {
			selected = repo
		}
	}
	payload.Repositories = []Repository{selected}
	payload.PreferredRepository = selected.FullName
	payload.Issues = make([]Issue, len(candidates))
	copy(payload.Issues, candidates)
	for i := range payload.Issues {
		payload.Issues[i].Body = truncate(RedactSecrets(payload.Issues[i].Body), 4000)
	}
	proposal, err := s.Model.Propose(ctx, payload)
	if err != nil {
		return Result{}, err
	}
	if err = ValidateProposal(proposal, []string{selected.FullName}, candidates); err != nil {
		return Result{}, err
	}
	if proposal.Question != "" || proposal.Confidence < .85 {
		return question(proposal.Question, "Уточните содержание задачи."), nil
	}
	if proposal.DuplicateNumber != nil {
		for _, issue := range candidates {
			if issue.Number == *proposal.DuplicateNumber {
				return Result{Kind: "duplicate", URL: issue.URL, Proposal: Proposal{Title: issue.Title, Body: issue.Body}}, nil
			}
		}
	}
	proposal.Title = RedactSecrets(proposal.Title)
	proposal.Body = strings.TrimSpace(sourceTags.ReplaceAllString(RedactSecrets(proposal.Body), ""))
	if err = ValidateProposal(proposal, []string{selected.FullName}, candidates); err != nil {
		return Result{}, err
	}
	bytes := make([]byte, 18)
	if _, err = rand.Read(bytes); err != nil {
		return Result{}, err
	}
	draft := Draft{Token: hex.EncodeToString(bytes), Source: marker, Fingerprint: fingerprint, State: "preview", UserID: r.UserID, ChatID: r.ChatID, Proposal: proposal, Private: selected.Private, Created: s.now()}
	if err = s.Store.Save(draft); err != nil {
		return Result{}, err
	}
	return resultFromDraft(draft), nil
}
func question(text, fallback string) Result {
	if text == "" {
		text = fallback
	}
	return Result{Kind: "question", Text: text}
}

func (s *Service) Confirm(ctx context.Context, token string, userID, chatID int64) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, exists := s.Store.Get(token)
	policy, repos, allowed, err := s.resolvePolicy(ctx, userID, chatID)
	if err != nil {
		return Result{}, err
	}
	if !exists || !allowed || draft.UserID != userID || draft.ChatID != chatID || !s.draftVisible(draft, policy, repos, chatID) {
		return Result{Kind: "denied"}, nil
	}
	if draft.State != "preview" {
		return resultFromDraft(draft), nil
	}
	if s.now().Sub(draft.Created) > 24*time.Hour {
		_, err := s.Store.Transition(token, "preview", "expired", "")
		return Result{Kind: "expired"}, err
	}
	p := draft.Proposal
	if !containsRepository(policy.Repositories, p.Repository) {
		return Result{Kind: "denied"}, nil
	}
	if draft.EditTarget != nil {
		return s.confirmEdit(ctx, draft)
	}
	candidates, err := s.Github.Candidates(ctx, p.Repository, p.Title)
	if err != nil {
		return Result{}, err
	}
	for _, issue := range candidates {
		if strings.Contains(issue.Body, draft.Source) {
			_, err = s.Store.Transition(token, "preview", "created", issue.URL)
			return Result{Kind: "created", URL: issue.URL, Proposal: Proposal{Title: issue.Title, Body: issue.Body}}, err
		}
	}
	claimed, err := s.Store.Transition(token, "preview", "creating", "")
	if err != nil {
		return Result{}, err
	}
	if !claimed {
		return Result{}, fmt.Errorf("draft changed")
	}
	url, createErr := s.Github.Create(ctx, p.Repository, p.Title, p.Body+"\n\n<!-- "+draft.Source+" -->")
	state := "created"
	if createErr != nil {
		state = "uncertain"
		url = ""
	}
	if _, err = s.Store.Transition(token, "creating", state, url); err != nil {
		return Result{Kind: "uncertain"}, err
	}
	return Result{Kind: state, URL: url, Proposal: p}, nil
}
func (s *Service) Cancel(token string, userID, chatID int64) (Result, error) {
	return s.CancelWithContext(context.Background(), token, userID, chatID)
}
func (s *Service) CancelWithContext(ctx context.Context, token string, userID, chatID int64) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, exists := s.Store.Get(token)
	policy, repos, allowed, err := s.resolvePolicy(ctx, userID, chatID)
	if err != nil {
		return Result{}, err
	}
	if !exists || !allowed || draft.UserID != userID || draft.ChatID != chatID || !s.draftVisible(draft, policy, repos, chatID) {
		return Result{Kind: "denied"}, nil
	}
	_, err = s.Store.Transition(token, "preview", "cancelled", "")
	if err != nil {
		return Result{}, err
	}
	draft, _ = s.Store.Get(token)
	return resultFromDraft(draft), nil
}

// Exact policies override wildcard policies even when they deny this user.
func (s *Service) policyFor(chatID int64) (Policy, bool, bool) {
	if p, ok := s.Config[strconv.FormatInt(chatID, 10)]; ok {
		return p, true, false
	}
	p, ok := s.Config["*"]
	return p, ok, true
}
func (s *Service) resolvePolicy(ctx context.Context, userID, chatID int64) (Policy, []Repository, bool, error) {
	p, ok, wildcard := s.policyFor(chatID)
	if !ok || !slices.Contains(p.Users, userID) {
		return p, nil, false, nil
	}
	repositories, err := s.Github.Repositories(ctx, p.Repositories)
	if err != nil {
		return p, nil, false, err
	}
	var resolved []Repository
	var names []string
	discover := len(p.Repositories) == 1 && p.Repositories[0] == "*"
	for _, repository := range repositories {
		if (!discover && !containsRepository(p.Repositories, repository.FullName)) || !validRepositoryName(repository.FullName) {
			continue
		}
		if wildcard && repository.Private && !p.AllowPrivateInAnyChat {
			continue
		}
		resolved = append(resolved, repository)
		names = append(names, repository.FullName)
	}
	p.Repositories = names
	p.DefaultRepository, _ = canonicalRepository(names, p.DefaultRepository)
	return p, resolved, len(resolved) > 0, nil
}
func (s *Service) draftVisible(d Draft, p Policy, repositories []Repository, chatID int64) bool {
	_, _, wildcard := s.policyFor(chatID)
	if wildcard && d.Private && !p.AllowPrivateInAnyChat {
		return false
	}
	for _, repository := range repositories {
		if strings.EqualFold(repository.FullName, d.Proposal.Repository) && repository.Private == d.Private {
			return true
		}
	}
	return false
}
