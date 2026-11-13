package taskbot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type editGithub struct {
	fakeGithub
	issue   Issue
	updates int
}

func (g *editGithub) GetIssue(context.Context, string, int) (Issue, error) { return g.issue, nil }
func (g *editGithub) UpdateIssue(_ context.Context, _ string, _ int, title, body string) error {
	g.updates++
	if g.fail {
		return errors.New("timeout")
	}
	g.issue.Title = title
	g.issue.Body = body
	return nil
}
func TestEditRequiresConfirmationAndDetectsConcurrentChanges(t *testing.T) {
	s, _, r := fixture(t)
	g := &editGithub{issue: Issue{Number: 9, Title: "Old", Body: "Original", URL: "https://github.com/acme/paint/issues/9"}}
	s.Github = g
	r.Edit = true
	r.Instruction = "/edit https://github.com/acme/paint/issues/9 исправь описание"
	preview, err := s.Prepare(context.Background(), r)
	if err != nil || preview.Kind != "preview" || g.updates != 0 {
		t.Fatal(preview, err)
	}
	g.issue.Body = "Changed by someone"
	result, err := s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
	if err != nil || result.Kind != "expired" || g.updates != 0 {
		t.Fatal(result, err)
	}
}
func TestEditOutcomeAndNoRetry(t *testing.T) {
	for _, fail := range []bool{false, true} {
		s, _, r := fixture(t)
		g := &editGithub{issue: Issue{Number: 9, Title: "Old", Body: "Original", URL: "https://github.com/acme/paint/issues/9"}}
		s.Github = g
		r.Edit = true
		r.Instruction = "/edit https://github.com/acme/paint/issues/9 исправь описание"
		preview, err := s.Prepare(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		g.fail = fail
		result, err := s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
		expected := "edited"
		if fail {
			expected = "uncertain"
		}
		if err != nil || result.Kind != expected || (!fail && result.URL != g.issue.URL) {
			t.Fatal(result, err)
		}
		_, _ = s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
		if g.updates != 1 {
			t.Fatal("repeated PATCH")
		}
	}
}

func TestEditPreservesSourceMarker(t *testing.T) {
	s, _, r := fixture(t)
	marker := "<!-- " + SourceMarker(r.ChatID, 99) + " -->"
	g := &editGithub{issue: Issue{Number: 9, Title: "Old", Body: "Original\n" + marker, URL: "https://github.com/acme/paint/issues/9"}}
	s.Github = g
	r.Edit = true
	r.Instruction = "/edit https://github.com/acme/paint/issues/9 исправь описание"
	preview, err := s.Prepare(context.Background(), r)
	if err != nil || !strings.Contains(preview.Proposal.Body, marker) {
		t.Fatal(preview, err)
	}
	result, err := s.Confirm(context.Background(), preview.Token, r.UserID, r.ChatID)
	if err != nil || result.Kind != "edited" || !strings.Contains(g.issue.Body, marker) {
		t.Fatal(result, err)
	}
}
