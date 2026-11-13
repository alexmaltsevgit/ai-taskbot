package taskbot

import "testing"

func TestExtractRequest(t *testing.T) {
	source := &Message{ID: 3, Text: "Save fails"}
	m := Message{ID: 8, From: &User{ID: 7}, Chat: Chat{ID: -1001, Title: "Paint"}, Text: "😀 @pilot_bot create issue", Entities: []Entity{{Type: "mention", Offset: 3, Length: 10}}, Reply: source}
	if got := ExtractRequest(m, "pilot_bot"); got == nil || got.Source != "Save fails" {
		t.Fatal("mention + reply not parsed")
	}
	if ExtractRequest(m, "other_bot") != nil {
		t.Fatal("wrong bot accepted")
	}
	m.SenderChat = &Chat{ID: -10}
	if ExtractRequest(m, "pilot_bot") != nil {
		t.Fatal("anonymous sender accepted")
	}
	m.SenderChat = nil
	m.From.IsBot = true
	if ExtractRequest(m, "pilot_bot") != nil {
		t.Fatal("bot accepted")
	}
}
func TestExplicitRepository(t *testing.T) {
	repo, err := ExplicitRepository("create in acme/paint", []string{"acme/paint"})
	if err != nil || repo != "acme/paint" {
		t.Fatal(repo, err)
	}
	if _, err := ExplicitRepository("other/private", []string{"acme/paint"}); err == nil {
		t.Fatal("unlisted repo allowed")
	}
}
func TestProposalValidation(t *testing.T) {
	p := Proposal{Repository: "acme/paint", Confidence: .99, Title: "Fix", Body: "Expected: save"}
	if err := ValidateProposal(p, []string{"acme/paint"}, nil); err != nil {
		t.Fatal(err)
	}
	n := 999
	p.DuplicateNumber = &n
	if ValidateProposal(p, []string{"acme/paint"}, nil) == nil {
		t.Fatal("invented duplicate accepted")
	}
}
func TestRedaction(t *testing.T) {
	got := RedactSecrets("password=hunter2 token=abcdef")
	if got == "password=hunter2 token=abcdef" {
		t.Fatal("secrets unchanged")
	}
}

func TestExplicitRepositoryPreservesTrailingPunctuationInNames(t *testing.T) {
	for _, repo := range []string{"acme/repo-", "acme/repo."} {
		got, err := ExplicitRepository("create in "+repo, []string{"acme/repo", repo})
		if err != nil || got != repo {
			t.Fatalf("wanted %s, got %s: %v", repo, got, err)
		}
	}
}
