package taskbot

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

const historySettingsLimit = 2000

type historyScope struct{ ChatID, ThreadID int64 }
type historySetting struct {
	Enabled        bool
	ActorID        int64
	Started, Until time.Time
	AfterID        int64
}

// Only a leading Telegram bot_command entity authorizes a context change.
// A mention, quote, forwarded instruction or command for another bot does not.
func ExtractContextCommand(m Message, bot string) (string, bool) {
	if m.From == nil || m.From.ID <= 0 || m.From.IsBot || m.SenderChat != nil || m.ForwardOrigin != nil {
		return "", false
	}
	units := utf16.Encode([]rune(m.Text))
	for _, entity := range m.Entities {
		if entity.Type != "bot_command" || entity.Offset != 0 || entity.Length <= 0 || entity.Length > len(units) {
			continue
		}
		token := strings.ToLower(string(utf16.Decode(units[:entity.Length])))
		if token != "/context" && token != "/context@"+strings.ToLower(bot) {
			continue
		}
		suffix := string(utf16.Decode(units[entity.Length:]))
		if suffix != "" && !unicode.IsSpace([]rune(suffix)[0]) {
			return "", true
		}
		fields := strings.Fields(suffix)
		if len(fields) != 1 {
			return "", true
		}
		action := strings.ToLower(fields[0])
		if action != "on" && action != "off" && action != "status" {
			return "", true
		}
		return action, true
	}
	return "", false
}

// Caller holds historyMu. Off overrides configured defaults until restart/on;
// expired/revoked opt-ins must not fall back to a configured on default.
func (s *Service) historyEnabled(scope historyScope, now time.Time) bool {
	policy, ok, wildcard := s.policyFor(scope.ChatID)
	if !ok || len(policy.Users) == 0 {
		return false
	}
	if setting, exists := s.historySettings[scope]; exists {
		_, allowed := s.Allowed(setting.ActorID, scope.ChatID)
		return setting.Enabled && allowed && now.Before(setting.Until)
	}
	return !wildcard && policy.HistoryEnabled
}
func (s *Service) purgeScope(scope historyScope) {
	kept := make([]historyEntry, 0, len(s.history))
	for _, entry := range s.history {
		if entry.ChatID != scope.ChatID || entry.ThreadID != scope.ThreadID {
			kept = append(kept, entry)
		}
	}
	s.history = kept
}
func (s *Service) ManageContext(m Message, action string) Result {
	s.historyMu.Lock()
	defer s.historyMu.Unlock()
	now := s.now()
	s.pruneHistory(now)
	if m.From == nil || m.From.IsBot || m.SenderChat != nil || m.From.ID <= 0 || m.ForwardOrigin != nil {
		return Result{Kind: "denied"}
	}
	if _, allowed := s.Allowed(m.From.ID, m.Chat.ID); !allowed {
		return Result{Kind: "denied"}
	}
	scope := historyScope{m.Chat.ID, m.ThreadID}
	if action == "status" {
		if !s.historyEnabled(scope, now) {
			return Result{Kind: "question", Text: "Контекст в этом чате/теме выключен. /context on включит сбор будущих сообщений на 24 часа."}
		}
		text := "Контекст в этом чате/теме включён настройкой чата; сообщения хранятся в памяти не дольше 24 часов. /context off остановит сбор и очистит этот буфер."
		if setting, ok := s.historySettings[scope]; ok {
			text = fmt.Sprintf("Контекст в этом чате/теме включён до %s UTC; только доступные боту сообщения, не полная история. /context off остановит сбор и очистит буфер.", setting.Until.UTC().Format("2006-01-02 15:04"))
		}
		return Result{Kind: "question", Text: text}
	}
	if action != "on" && action != "off" {
		return Result{Kind: "question", Text: "Используйте /context on, /context off или /context status в начале сообщения."}
	}
	if s.historySettings == nil {
		s.historySettings = map[historyScope]historySetting{}
	}
	if _, exists := s.historySettings[scope]; !exists && len(s.historySettings) >= historySettingsLimit {
		return Result{Kind: "question", Text: "Достигнут лимит настроек контекста. Администратору нужно проверить активные чаты; новый сбор не включён."}
	}
	s.purgeScope(scope)
	s.historySettings[scope] = historySetting{Enabled: action == "on", ActorID: m.From.ID, Started: now, Until: now.Add(historyLifetime), AfterID: m.ID}
	if action == "off" {
		return Result{Kind: "question", Text: "Контекст в этом чате/теме выключен, буфер обсуждения очищен. Это не удаляет уже опубликованные issues и сохранённые операции."}
	}
	return Result{Kind: "question", Text: "Включаю контекст только в этом чате/теме на 24 часа. Доступные боту будущие сообщения участников будут временно храниться в памяти; релевантные фрагменты могут отправляться модели и входить в подтверждённую GitHub-задачу. Прошлую историю не загружаю. Лимит: 200 сообщений на тему, 2000 суммарно; хранение до 24 часов, после перезапуска буфер исчезает. Участники должны быть согласны. /context off остановит сбор и очистит буфер."}
}
