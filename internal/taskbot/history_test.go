package taskbot

import (
	"testing"
	"time"
)

func TestRequestWithoutReply(t *testing.T) {
	m := Message{ID: 10, From: &User{ID: 1}, Text: "@pilot_bot исправь оплату", Entities: []Entity{{Type: "mention", Length: 10}}}
	r := ExtractRequest(m, "pilot_bot")
	if r == nil || r.SourceID != 10 || r.Source != m.Text {
		t.Fatalf("standalone request: %+v", r)
	}
}
func TestHistoryBoundariesAndRedaction(t *testing.T) {
	now := time.Unix(1700000000, 0)
	s := Service{Config: Config{"1": Policy{HistoryEnabled: true}}, Now: func() time.Time { return now }}
	for _, m := range []Message{
		{ID: 1, Date: now.Unix(), Chat: Chat{ID: 1}, ThreadID: 7, From: &User{ID: 2}, Text: "password=abc payment broken"},
		{ID: 2, Date: now.Unix(), Chat: Chat{ID: 2}, ThreadID: 7, From: &User{ID: 2}, Text: "other chat"},
		{ID: 3, Date: now.Unix(), Chat: Chat{ID: 1}, ThreadID: 8, From: &User{ID: 2}, Text: "other topic"},
		{ID: 4, Date: now.Add(-25 * time.Hour).Unix(), Chat: Chat{ID: 1}, ThreadID: 7, From: &User{ID: 2}, Text: "old"},
	} {
		s.Remember(m)
	}
	r := Request{ChatID: 1, ThreadID: 7, MessageID: 5, SourceID: 5}
	got := s.Context(r)
	if len(got) != 1 || got[0].Text != "[REDACTED] payment broken" {
		t.Fatalf("%+v", got)
	}
	now = now.Add(25 * time.Hour)
	if len(s.Context(r)) != 0 {
		t.Fatal("expired history retained")
	}
}
func TestHistoryEditsAndOptIn(t *testing.T) {
	now := time.Now()
	s := Service{Config: Config{"1": Policy{HistoryEnabled: true}}, Now: func() time.Time { return now }}
	m := Message{ID: 1, Date: now.Unix(), Chat: Chat{ID: 1}, From: &User{ID: 2}, Text: "old text"}
	s.Remember(m)
	m.Text = "corrected"
	s.Remember(m)
	got := s.Context(Request{ChatID: 1, MessageID: 2, SourceID: 2})
	if len(got) != 1 || got[0].Text != "corrected" {
		t.Fatalf("%+v", got)
	}
	p := s.Config["1"]
	p.HistoryEnabled = false
	s.Config["1"] = p
	if len(s.Context(Request{ChatID: 1, MessageID: 2})) != 0 {
		t.Fatal("disabled context")
	}
}

func TestHistoryBoundsAndReplyAncestors(t *testing.T) {
	now := time.Now()
	s := Service{Config: Config{"1": Policy{HistoryEnabled: true}}, Now: func() time.Time { return now }}
	for i := int64(1); i <= 210; i++ {
		s.Remember(Message{ID: i, Date: now.Unix(), Chat: Chat{ID: 1}, From: &User{ID: 2}, Text: "text"})
	}
	if len(s.history) != historyTopicLimit {
		t.Fatal(len(s.history))
	}
	got := s.Context(Request{ChatID: 1, MessageID: 211, SourceID: 211})
	if len(got) != contextLimit || got[0].ID != 181 {
		t.Fatalf("%+v", got)
	}
	s.history = nil
	old := Message{ID: 1, Date: now.Add(-3 * time.Hour).Unix(), Chat: Chat{ID: 1}, From: &User{ID: 2}, Text: "original"}
	s.Remember(old)
	s.Remember(Message{ID: 2, Date: now.Unix(), Chat: Chat{ID: 1}, From: &User{ID: 2}, Text: "correction", Reply: &old})
	got = s.Context(Request{ChatID: 1, MessageID: 3, SourceID: 2})
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("missing explicit ancestor: %+v", got)
	}
}
func TestEditCommandAndOtherBot(t *testing.T) {
	m := Message{ID: 1, From: &User{ID: 2}, Text: "/edit@pilot_bot https://github.com/acme/paint/issues/9 change", Entities: []Entity{{Type: "bot_command", Length: 15}}}
	if r := ExtractRequest(m, "pilot_bot"); r == nil || !r.Edit {
		t.Fatalf("%+v", r)
	}
	if r := ExtractRequest(m, "other_bot"); r != nil {
		t.Fatal("other bot command accepted")
	}
}

func TestEmbeddedEditIsNotAuthorization(t *testing.T) {
	m := Message{ID: 1, From: &User{ID: 2}, Text: "/issue@pilot_bot describe /edit https://github.com/acme/paint/issues/9", Entities: []Entity{{Type: "bot_command", Offset: 0, Length: 16}, {Type: "bot_command", Offset: 26, Length: 5}}}
	// Derive offsets in UTF-16, just as Telegram does.
	m.Entities[0].Length = len("/issue@pilot_bot")
	m.Entities[1].Offset = len("/issue@pilot_bot describe ")
	r := ExtractRequest(m, "pilot_bot")
	if r == nil || r.Edit {
		t.Fatalf("embedded edit authorized: %+v", r)
	}
}
