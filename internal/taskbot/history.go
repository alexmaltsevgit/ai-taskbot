package taskbot

import (
	"context"
	"slices"
	"sort"
	"time"
)

// Ambient discussion never enters durable state. Limits also apply across topics,
// so an active group cannot grow memory indefinitely by creating new topics.
const (
	historyLifetime   = 24 * time.Hour
	historyLimit      = 2000
	historyTopicLimit = 200
	contextLimit      = 30
	contextRuneLimit  = 16000
)

type ContextMessage struct {
	ID      int64  `json:"message_id"`
	ReplyID int64  `json:"reply_to_message_id,omitempty"`
	Date    int64  `json:"date"`
	Text    string `json:"text"`
}
type historyEntry struct {
	ChatID, ThreadID int64
	ContextMessage
}

func (s *Service) pruneHistory(now time.Time) {
	for scope, setting := range s.historySettings {
		_, allowed := s.Allowed(setting.ActorID, scope.ChatID)
		if setting.Enabled && (!allowed || !now.Before(setting.Until)) {
			setting.Enabled = false
			s.historySettings[scope] = setting
		}
	}
	filtered := make([]historyEntry, 0, len(s.history))
	for _, entry := range s.history {
		if s.historyEnabled(historyScope{entry.ChatID, entry.ThreadID}, now) && now.Sub(time.Unix(entry.Date, 0)) <= historyLifetime {
			filtered = append(filtered, entry)
		}
	}
	s.history = filtered
}
func (s *Service) Remember(m Message) {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	now := s.now()
	s.pruneHistory(now)
	scope := historyScope{m.Chat.ID, m.ThreadID}
	if !s.historyEnabled(scope, now) || m.From == nil || m.From.IsBot || m.SenderChat != nil || m.Date <= 0 || m.ID <= 0 {
		return
	}
	if setting, ok := s.historySettings[scope]; ok && (m.ID <= setting.AfterID || m.Date < setting.Started.Unix()) {
		return
	}
	age := now.Sub(time.Unix(m.Date, 0))
	if age < 0 || age > historyLifetime {
		return
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	// Remove an old version even when an edit removes all text.
	for i, entry := range s.history {
		if entry.ChatID == m.Chat.ID && entry.ID == m.ID {
			s.history = slices.Delete(s.history, i, i+1)
			break
		}
	}
	if text == "" {
		return
	}
	entry := historyEntry{ChatID: m.Chat.ID, ThreadID: m.ThreadID, ContextMessage: ContextMessage{ID: m.ID, Date: m.Date, Text: truncate(RedactSecrets(text), 2000)}}
	if m.Reply != nil {
		entry.ReplyID = m.Reply.ID
	}
	s.history = append(s.history, entry)
	sort.Slice(s.history, func(i, j int) bool {
		return s.history[i].Date < s.history[j].Date || (s.history[i].Date == s.history[j].Date && s.history[i].ID < s.history[j].ID)
	})
	count := 0
	for i := len(s.history) - 1; i >= 0; i-- {
		e := s.history[i]
		if e.ChatID == m.Chat.ID && e.ThreadID == m.ThreadID {
			count++
			if count > historyTopicLimit {
				s.history = slices.Delete(s.history, i, i+1)
			}
		}
	}
	if len(s.history) > historyLimit {
		s.history = slices.Clone(s.history[len(s.history)-historyLimit:])
	}
}
func (s *Service) Context(r Request) []ContextMessage {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	now := s.now()
	s.pruneHistory(now)
	_, allowed := s.Allowed(r.UserID, r.ChatID)
	if !allowed || !s.historyEnabled(historyScope{r.ChatID, r.ThreadID}, now) {
		return nil
	}
	var topic []ContextMessage
	for _, entry := range s.history {
		if entry.ChatID == r.ChatID && entry.ThreadID == r.ThreadID && entry.ID < r.MessageID {
			topic = append(topic, entry.ContextMessage)
		}
	}
	// First retain the explicit reply chain (when available), then fill with recent
	// discussion. The model must select relevant facts, not merge unrelated topics.
	selected := map[int64]bool{}
	anchor := r.SourceID
	for depth := 0; depth < 10; depth++ {
		found := false
		for _, entry := range topic {
			if entry.ID == anchor {
				selected[entry.ID] = true
				anchor = entry.ReplyID
				found = true
				break
			}
		}
		if !found || anchor == 0 {
			break
		}
	}
	for i := len(topic) - 1; i >= 0 && len(selected) < contextLimit; i-- {
		if now.Sub(time.Unix(topic[i].Date, 0)) <= 2*time.Hour {
			selected[topic[i].ID] = true
		}
	}
	// Preserve chronology, favor the most recent content if the text budget fills.
	var result []ContextMessage
	budget := contextRuneLimit
	for i := len(topic) - 1; i >= 0; i-- {
		entry := topic[i]
		if !selected[entry.ID] || entry.ID == r.SourceID {
			continue
		}
		n := len([]rune(entry.Text))
		if n > budget {
			continue
		}
		result = append(result, entry)
		budget -= n
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// Expire ambient text even when the chat is silent or model inference is running.
func (s *Service) expireHistory(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.historyMu.Lock()
			s.pruneHistory(s.now())
			s.historyMu.Unlock()
		}
	}
}
