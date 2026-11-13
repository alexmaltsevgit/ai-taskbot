package taskbot

import (
	"context"
	"errors"
	"testing"
	"time"
)

func contextMessage(id, chat, topic, user int64, text string) Message {
	return Message{ID: id, Chat: Chat{ID: chat}, ThreadID: topic, From: &User{ID: user}, Text: text, Entities: []Entity{{Type: "bot_command", Offset: 0, Length: len("/context")}}}
}
func TestContextCommandStrictness(t *testing.T) {
	m := contextMessage(1, 10, 2, 1, "/context on")
	if a, ok := ExtractContextCommand(m, "bot"); !ok || a != "on" {
		t.Fatal(a, ok)
	}
	m.Text = "/context@bot off"
	m.Entities[0].Length = len("/context@bot")
	if a, ok := ExtractContextCommand(m, "bot"); !ok || a != "off" {
		t.Fatal(a, ok)
	}
	if _, ok := ExtractContextCommand(m, "other"); ok {
		t.Fatal("other bot")
	}
	for _, text := range []string{"/contexton", "/context on extra", "/context unknown"} {
		m = contextMessage(1, 10, 2, 1, text)
		if a, _ := ExtractContextCommand(m, "bot"); a != "" {
			t.Fatal(text, a)
		}
	}
	for _, change := range []func(*Message){func(m *Message) { m.Entities[0].Offset = 1 }, func(m *Message) { m.Entities[0].Type = "mention" }, func(m *Message) { m.From.IsBot = true }, func(m *Message) { m.SenderChat = &Chat{ID: 2} }, func(m *Message) { m.ForwardOrigin = map[string]any{"type": "user"} }} {
		m = contextMessage(1, 10, 2, 1, "/context on")
		change(&m)
		if _, ok := ExtractContextCommand(m, "bot"); ok {
			t.Fatal("unsafe command", m)
		}
	}
}
func TestWildcardContextScopeExpiryAndRevocation(t *testing.T) {
	now := time.Unix(1700000000, 0)
	s := Service{Config: Config{"*": {Users: []int64{1}, Repositories: []string{"acme/paint"}}}, Now: func() time.Time { return now }}
	command := contextMessage(10, 10, 2, 1, "/context on")
	message := Message{ID: 11, Date: now.Unix(), Chat: Chat{ID: 10}, ThreadID: 2, From: &User{ID: 2}, Text: "future fact"}
	s.Remember(message)
	if len(s.history) != 0 {
		t.Fatal("passive wildcard collection")
	}
	if result := s.ManageContext(command, "on"); result.Kind != "question" {
		t.Fatal(result)
	}
	old := message
	old.ID = 9
	s.Remember(old)
	other := message
	other.ThreadID = 3
	s.Remember(other)
	s.Remember(message)
	request := Request{UserID: 1, ChatID: 10, ThreadID: 2, MessageID: 12, SourceID: 12}
	if got := s.Context(request); len(got) != 1 {
		t.Fatal(got)
	}
	denied := request
	denied.UserID = 2
	if len(s.Context(denied)) != 0 {
		t.Fatal("unauthorized reader")
	}
	now = now.Add(24 * time.Hour)
	if len(s.Context(request)) != 0 {
		t.Fatal("activation did not expire")
	}
	now = now.Add(time.Second)
	command.ID = 20
	s.ManageContext(command, "on")
	message.ID = 21
	message.Date = now.Unix()
	s.Remember(message)
	p := s.Config["*"]
	p.Users = []int64{3}
	s.Config["*"] = p
	s.Context(request)
	if len(s.history) != 0 {
		t.Fatal("revoked activation retained history")
	}
	p.Users = []int64{1}
	s.Config["*"] = p
	message.ID = 22
	s.Remember(message)
	if len(s.history) != 0 {
		t.Fatal("revoked activation resumed automatically")
	}
}
func TestContextOffOverridesExactDefaultAndIsTopicScoped(t *testing.T) {
	now := time.Now()
	s := Service{Config: Config{"10": {Users: []int64{1}, Repositories: []string{"acme/paint"}, HistoryEnabled: true}}, Now: func() time.Time { return now }}
	for _, topic := range []int64{1, 2} {
		s.Remember(Message{ID: topic, Date: now.Unix(), Chat: Chat{ID: 10}, ThreadID: topic, From: &User{ID: 2}, Text: "fact"})
	}
	command := contextMessage(10, 10, 1, 1, "/context off")
	s.ManageContext(command, "off")
	s.Remember(Message{ID: 11, Date: now.Unix(), Chat: Chat{ID: 10}, ThreadID: 1, From: &User{ID: 2}, Text: "not collected"})
	if len(s.history) != 1 || s.history[0].ThreadID != 2 {
		t.Fatal(s.history)
	}
	command.From.ID = 2
	if result := s.ManageContext(command, "on"); result.Kind != "denied" {
		t.Fatal(result)
	}
}

type failingContextTelegram struct{}

func (failingContextTelegram) Call(context.Context, string, any, any) error {
	return errors.New("send failed")
}
func TestContextNoticeFailureDisablesCollection(t *testing.T) {
	s := Service{Config: Config{"*": {Users: []int64{1}, Repositories: []string{"acme/paint"}}}}
	m := contextMessage(1, 10, 2, 1, "/context on")
	if err := HandleUpdate(context.Background(), Update{Message: &m}, &s, failingContextTelegram{}, "bot"); err == nil {
		t.Fatal("expected send error")
	}
	if s.historyEnabled(historyScope{10, 2}, time.Now()) {
		t.Fatal("collection enabled without visible notice")
	}
}

func TestContextHandlerNoticeAndScopedThread(t *testing.T) {
	s := Service{Config: Config{"*": {Users: []int64{1}, Repositories: []string{"acme/paint"}}}}
	telegram := &payloadTelegram{}
	m := contextMessage(1, 10, 2, 1, "/context on")
	if err := HandleUpdate(context.Background(), Update{Message: &m}, &s, telegram, "bot"); err != nil {
		t.Fatal(err)
	}
	if len(telegram.messages) != 1 || telegram.messages[0]["message_thread_id"] != int64(2) {
		t.Fatal(telegram.messages)
	}
	if !s.historyEnabled(historyScope{10, 2}, time.Now()) || s.historyEnabled(historyScope{10, 3}, time.Now()) {
		t.Fatal("scope error")
	}
	m.Text = "/context off"
	if err := HandleUpdate(context.Background(), Update{Message: &m}, &s, failingContextTelegram{}, "bot"); err == nil {
		t.Fatal("expected failed notice")
	}
	if s.historyEnabled(historyScope{10, 2}, time.Now()) {
		t.Fatal("off was undone by failed notice")
	}
}
