package taskbot

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

func ExtractRequest(m Message, bot string) *Request {
	if m.From == nil || m.From.ID <= 0 || m.From.IsBot || m.SenderChat != nil || m.Reply == nil {
		return nil
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	units := utf16.Encode([]rune(text))
	addressed := false
	for _, entity := range entities {
		if entity.Offset < 0 || entity.Length < 0 || entity.Offset > len(units) || entity.Length > len(units)-entity.Offset {
			continue
		}
		token := strings.ToLower(string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length])))
		if entity.Type == "mention" && token == "@"+strings.ToLower(bot) {
			addressed = true
		}
		if entity.Type == "bot_command" && (token == "/issue" || token == "/issue@"+strings.ToLower(bot)) {
			addressed = true
		}
	}
	if !addressed {
		return nil
	}
	source := m.Reply.Text
	if source == "" {
		source = m.Reply.Caption
	}
	return &Request{ChatID: m.Chat.ID, UserID: m.From.ID, MessageID: m.ID, SourceID: m.Reply.ID,
		ChatTitle: m.Chat.Title, Instruction: truncate(text, 8000), Source: truncate(source, 16000),
		HasAttachment: m.Reply.Photo != nil || m.Reply.Document != nil || m.Reply.Video != nil || m.Reply.Voice != nil || m.Reply.Audio != nil}
}

var repoPattern = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+`)
var validRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func ExplicitRepository(instruction string, allowed []string) (string, error) {
	matches := repoPattern.FindAllString(instruction, -1)
	if len(matches) == 0 {
		return "", nil
	}
	unique := map[string]bool{}
	for _, match := range matches {
		unique[strings.ToLower(match)] = true
	}
	if len(unique) != 1 {
		return "", fmt.Errorf("specify one repository")
	}
	for _, repo := range allowed {
		if unique[strings.ToLower(repo)] {
			return repo, nil
		}
	}
	return "", fmt.Errorf("repository is not allowed")
}
func ValidateProposal(p Proposal, repos []string, issues []Issue) error {
	if !slices.Contains(repos, p.Repository) {
		return fmt.Errorf("invalid repository")
	}
	if math.IsNaN(p.Confidence) || math.IsInf(p.Confidence, 0) || p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("invalid confidence")
	}
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Body) == "" || len([]rune(p.Title)) > 200 || len([]rune(p.Body)) > 12000 || len([]rune(p.Question)) > 1000 {
		return fmt.Errorf("invalid issue text")
	}
	if p.DuplicateNumber != nil {
		found := false
		for _, issue := range issues {
			if issue.Number == *p.DuplicateNumber {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unknown duplicate")
		}
	}
	return nil
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{20,}\b`), regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]+`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), regexp.MustCompile(`\b\d{6,12}:[A-Za-z0-9_-]{25,}`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|token|secret|api[_-]?key)\s*[=:]\s*[^\s,;]+`),
	regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`),
}

func RedactSecrets(text string) string {
	for _, pattern := range secretPatterns {
		text = pattern.ReplaceAllString(text, "[REDACTED]")
	}
	return text
}
func SourceMarker(chatID, messageID int64) string {
	return fmt.Sprintf("issue-pilot-source:%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d", chatID, messageID))))
}
func truncate(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:limit])
	}
	return text
}
func ValidateConfig(config Config) error {
	if len(config) == 0 {
		return fmt.Errorf("configure at least one chat")
	}
	for id, policy := range config {
		if _, err := strconv.ParseInt(id, 10, 64); err != nil {
			return fmt.Errorf("invalid chat ID")
		}
		if len(policy.Users) == 0 || len(policy.Repositories) == 0 {
			return fmt.Errorf("empty allowlist")
		}
		for _, id := range policy.Users {
			if id <= 0 {
				return fmt.Errorf("invalid user ID")
			}
		}
		for _, repo := range policy.Repositories {
			if !validRepo.MatchString(repo) {
				return fmt.Errorf("invalid repository")
			}
		}
		if policy.DefaultRepository != "" && !slices.Contains(policy.Repositories, policy.DefaultRepository) {
			return fmt.Errorf("default repository must be allowed")
		}
	}
	return nil
}
