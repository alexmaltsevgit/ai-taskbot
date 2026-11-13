package taskbot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type fakeGithub struct {
	created int
	fail    bool
	issues  []Issue
}

func (f *fakeGithub) Repositories(ctx context.Context, names []string) ([]Repository, error) {
	var r []Repository
	for _, n := range names {
		r = append(r, Repository{FullName: n, Private: true})
	}
	return r, nil
}
func (f *fakeGithub) Candidates(context.Context, string, string) ([]Issue, error) {
	return f.issues, nil
}
func (f *fakeGithub) Create(context.Context, string, string, string) (string, error) {
	if f.fail {
		return "", errors.New("timeout")
	}
	f.created++
	return "https://github.com/acme/paint/issues/1", nil
}

type fakeModel struct{ duplicate *int }

func (f fakeModel) Propose(ctx context.Context, p Payload) (Proposal, error) {
	d := f.duplicate
	if len(p.Issues) == 0 {
		d = nil
	}
	return Proposal{Repository: "acme/paint", Confidence: .99, Title: "Fix save", Body: "Save should work", DuplicateNumber: d}, nil
}
func fixture(t *testing.T) (*Service, *fakeGithub, Request) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	g := &fakeGithub{}
	s := &Service{Store: store, Github: g, Model: fakeModel{}, Config: Config{"-1001": {Users: []int64{7}, Repositories: []string{"acme/paint"}}}, Now: time.Now}
	return s, g, Request{ChatID: -1001, UserID: 7, MessageID: 8, SourceID: 3, Instruction: "create issue", Source: "save fails"}
}
func TestPreviewAndIdempotentConfirm(t *testing.T) {
	s, g, r := fixture(t)
	ctx := context.Background()
	a, e := s.Prepare(ctx, r)
	if e != nil || a.Kind != "preview" || g.created != 0 {
		t.Fatal(a, e)
	}
	b, e := s.Prepare(ctx, r)
	if e != nil || a.Token != b.Token {
		t.Fatal(b, e)
	}
	res, e := s.Confirm(ctx, a.Token, 7, -1001)
	if e != nil || res.Kind != "created" {
		t.Fatal(res, e)
	}
	_, _ = s.Confirm(ctx, a.Token, 7, -1001)
	if g.created != 1 {
		t.Fatal(g.created)
	}
}
func TestUnauthorizedConfirmation(t *testing.T) {
	s, g, r := fixture(t)
	a, _ := s.Prepare(context.Background(), r)
	for _, who := range [][2]int64{{8, -1001}, {7, -2}} {
		res, _ := s.Confirm(context.Background(), a.Token, who[0], who[1])
		if res.Kind != "denied" {
			t.Fatal(res)
		}
	}
	if g.created != 0 {
		t.Fatal("created")
	}
}
func TestUncertainWriteNotRetried(t *testing.T) {
	s, g, r := fixture(t)
	a, _ := s.Prepare(context.Background(), r)
	g.fail = true
	res, _ := s.Confirm(context.Background(), a.Token, 7, -1001)
	if res.Kind != "uncertain" {
		t.Fatal(res)
	}
	g.fail = false
	res, _ = s.Confirm(context.Background(), a.Token, 7, -1001)
	if res.Kind != "uncertain" || g.created != 0 {
		t.Fatal(res)
	}
}
func TestCancelledDraftCanBeReplaced(t *testing.T) {
	s, _, r := fixture(t)
	a, _ := s.Prepare(context.Background(), r)
	_, _ = s.Cancel(a.Token, 7, -1001)
	b, _ := s.Prepare(context.Background(), r)
	if b.Token == a.Token || b.Kind != "preview" {
		t.Fatal(b)
	}
	res, _ := s.Confirm(context.Background(), a.Token, 7, -1001)
	if res.Kind != "denied" {
		t.Fatal(res)
	}
}
func TestChangedInstructionReplacesPreview(t *testing.T) {
	s, _, r := fixture(t)
	a, _ := s.Prepare(context.Background(), r)
	r.Instruction = "add reproduction steps"
	b, _ := s.Prepare(context.Background(), r)
	if a.Token == b.Token {
		t.Fatal("old preview reused")
	}
}
func TestDuplicate(t *testing.T) {
	s, g, r := fixture(t)
	n := 4
	g.issues = []Issue{{Number: n, URL: "https://github.com/acme/paint/issues/4"}}
	s.Model = fakeModel{duplicate: &n}
	res, e := s.Prepare(context.Background(), r)
	if e != nil || res.Kind != "duplicate" || g.created != 0 {
		t.Fatal(res, e)
	}
}
func TestExpiredDraftRegenerated(t *testing.T) {
	s, _, r := fixture(t)
	a, _ := s.Prepare(context.Background(), r)
	s.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	b, _ := s.Prepare(context.Background(), r)
	if a.Token == b.Token {
		t.Fatal("expired token retained")
	}
}
