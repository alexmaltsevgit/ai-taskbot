package taskbot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPreviewIncludesWholeBody(t *testing.T) {
	messages := RenderResult(Result{Kind: "preview", Token: "abc", Proposal: Proposal{Repository: "acme/paint", Title: "Fix", Body: strings.Repeat("я", 6000)}})
	var combined string
	for _, m := range messages {
		combined += m.Text
		if len([]rune(m.Text)) > 3500 {
			t.Fatal("oversized message")
		}
	}
	if !strings.Contains(combined, "ПУБЛИЧНЫЙ") || strings.Count(combined, "я") < 6000 || messages[len(messages)-1].Buttons == nil {
		t.Fatal("incomplete preview")
	}
}

type fakeTelegram struct {
	ackFails bool
	calls    []string
}

func (f *fakeTelegram) Call(ctx context.Context, method string, input, output any) error {
	f.calls = append(f.calls, method)
	if method == "answerCallbackQuery" && f.ackFails {
		return errors.New("stale")
	}
	return nil
}
func TestStaleCallbackStillCreates(t *testing.T) {
	s, g, r := fixture(t)
	draft, _ := s.Prepare(context.Background(), r)
	telegram := &fakeTelegram{ackFails: true}
	update := Update{Callback: &Callback{ID: "old", From: User{ID: 7}, Data: "create:" + draft.Token, Message: Message{ID: 8, Chat: Chat{ID: -1001}}}}
	if err := HandleUpdate(context.Background(), update, s, telegram, "bot"); err != nil {
		t.Fatal(err)
	}
	if g.created != 1 {
		t.Fatal("confirmation dropped")
	}
}
