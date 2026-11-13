package taskbot

type User struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}
type Chat struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}
type Entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}
type Message struct {
	ID              int64    `json:"message_id"`
	From            *User    `json:"from"`
	Chat            Chat     `json:"chat"`
	SenderChat      *Chat    `json:"sender_chat"`
	Text            string   `json:"text"`
	Caption         string   `json:"caption"`
	Entities        []Entity `json:"entities"`
	CaptionEntities []Entity `json:"caption_entities"`
	Reply           *Message `json:"reply_to_message"`
	Photo           any      `json:"photo"`
	Document        any      `json:"document"`
	Video           any      `json:"video"`
	Voice           any      `json:"voice"`
	Audio           any      `json:"audio"`
}
type Request struct {
	ChatID, UserID, MessageID, SourceID int64
	ChatTitle, Instruction, Source      string
	HasAttachment                       bool
}
type Repository struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Private     bool   `json:"private"`
}
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"html_url"`
	State  string `json:"state"`
}
type Proposal struct {
	Repository      string  `json:"repository"`
	Confidence      float64 `json:"confidence"`
	Title           string  `json:"title"`
	Body            string  `json:"body"`
	DuplicateNumber *int    `json:"duplicate_number"`
	Question        string  `json:"question"`
}
type Payload struct {
	Instruction         string       `json:"instruction"`
	QuotedMessage       string       `json:"quoted_message"`
	ChatTitle           string       `json:"chat_title"`
	Repositories        []Repository `json:"repositories"`
	PreferredRepository string       `json:"preferred_repository"`
	HasUnreadAttachment bool         `json:"has_unread_attachment"`
	Issues              []Issue      `json:"issues"`
}
type Policy struct {
	Users             []int64  `json:"users"`
	Repositories      []string `json:"repositories"`
	DefaultRepository string   `json:"default_repository,omitempty"`
}
type Config map[string]Policy
type Result struct {
	Kind, Token, URL, Text string
	Proposal               Proposal
	Private                bool
}
