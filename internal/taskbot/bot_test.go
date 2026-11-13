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

func TestOutcomeLabelsLinkAndSummary(t *testing.T) {
	for kind, label := range map[string]string{"created": "Новая", "duplicate": "Дубль", "edited": "Отредактировано"} {
		result := Result{Kind: kind, URL: "https://github.com/acme/paint/issues/1", Proposal: Proposal{Title: "Исправить оплату", Body: "Ошибка при сохранении"}}
		text := RenderResult(result)[0].Text
		for _, expected := range []string{label, result.URL, result.Proposal.Title, result.Proposal.Body} {
			if !strings.Contains(text, expected) {
				t.Fatal(text)
			}
		}
	}
}

type payloadTelegram struct{ messages []map[string]any }

func (f *payloadTelegram) Call(_ context.Context, method string, input, output any) error {
	if method == "sendMessage" {
		f.messages = append(f.messages, input.(map[string]any))
	}
	return nil
}
func TestStandaloneAcknowledgmentTopicRouting(t *testing.T) {
	for _, topic := range []int64{0, 42} {
		s, _, _ := fixture(t)
		telegram := &payloadTelegram{}
		m := Message{ID: 8, ThreadID: topic, From: &User{ID: 7}, Chat: Chat{ID: -1001}, Text: "/issue@bot fix save", Entities: []Entity{{Type: "bot_command", Length: 10}}}
		if err := HandleUpdate(context.Background(), Update{Message: &m}, s, telegram, "bot"); err != nil {
			t.Fatal(err)
		}
		if len(telegram.messages) < 2 {
			t.Fatal("missing acknowledgment/preview")
		}
		for _, message := range telegram.messages {
			got, present := message["message_thread_id"]
			if topic == 0 && present {
				t.Fatal("zero topic sent")
			}
			if topic != 0 && (!present || got != topic) {
				t.Fatal("wrong topic", message)
			}
		}
	}
}
