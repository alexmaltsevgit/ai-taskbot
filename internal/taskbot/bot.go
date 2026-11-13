package taskbot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

type Outgoing struct {
	Text    string
	Buttons any
}
type Callback struct {
	ID      string  `json:"id"`
	From    User    `json:"from"`
	Data    string  `json:"data"`
	Message Message `json:"message"`
}
type Update struct {
	ID       int64     `json:"update_id"`
	Message  *Message  `json:"message"`
	Callback *Callback `json:"callback_query"`
}
type Telegram interface {
	Call(context.Context, string, any, any) error
}

func RenderResult(result Result) []Outgoing {
	text := ""
	var buttons any
	switch result.Kind {
	case "preview":
		visibility := "приватный"
		if !result.Private {
			visibility = "ПУБЛИЧНЫЙ, содержимое будет видно всем"
		}
		text = fmt.Sprintf("Репозиторий: %s (%s)\n\n%s\n\n%s", result.Proposal.Repository, visibility, result.Proposal.Title, result.Proposal.Body)
		buttons = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Создать issue", "callback_data": "create:" + result.Token}, {"text": "Отмена", "callback_data": "cancel:" + result.Token}}}}
	case "created":
		text = "Создано: " + result.URL
	case "duplicate":
		text = "Нашёл существующую задачу: " + result.URL
	case "question":
		text = result.Text
	case "denied":
		text = "Нет доступа к этой операции."
	case "cancelled":
		text = "Создание отменено."
	case "expired":
		text = "Черновик устарел. Повторите команду ответом на исходное сообщение."
	case "uncertain":
		text = "Результат создания не подтверждён. Я не повторяю запрос, чтобы не создать дубль. Проверьте issues и попросите администратора сверить запись."
	default:
		text = "Операция не завершена."
	}
	messages := []Outgoing{}
	var chunk strings.Builder
	units := 0
	for _, r := range text {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > 3500 {
			messages = append(messages, Outgoing{Text: chunk.String()})
			chunk.Reset()
			units = 0
		}
		chunk.WriteRune(r)
		units += size
	}
	if chunk.Len() > 0 {
		messages = append(messages, Outgoing{Text: chunk.String()})
	}
	if len(messages) == 0 {
		messages = append(messages, Outgoing{Text: "Операция не завершена."})
	}
	messages[len(messages)-1].Buttons = buttons
	return messages
}
func SendResult(ctx context.Context, telegram Telegram, chatID, messageID int64, result Result) error {
	for _, message := range RenderResult(result) {
		args := map[string]any{"chat_id": chatID, "text": message.Text, "link_preview_options": map[string]bool{"is_disabled": true}, "reply_parameters": map[string]any{"message_id": messageID, "allow_sending_without_reply": true}}
		if message.Buttons != nil {
			args["reply_markup"] = message.Buttons
		}
		if err := telegram.Call(ctx, "sendMessage", args, nil); err != nil {
			return err
		}
	}
	return nil
}
func HandleUpdate(ctx context.Context, update Update, service *Service, telegram Telegram, username string) error {
	if callback := update.Callback; callback != nil {
		chatID, userID := callback.Message.Chat.ID, callback.From.ID
		action, token, ok := strings.Cut(callback.Data, ":")
		if chatID == 0 || !ok || (action != "create" && action != "cancel") {
			return nil
		}
		// Telegram may reject an old callback ID after a slow inference request. That
		// must not discard the authorized, durable confirmation itself.
		if err := telegram.Call(ctx, "answerCallbackQuery", map[string]string{"callback_query_id": callback.ID}, nil); err != nil {
			log.Print("Callback acknowledgment unavailable; continuing confirmation")
		}
		var result Result
		var err error
		if action == "create" {
			result, err = service.Confirm(ctx, token, userID, chatID)
		} else {
			result, err = service.Cancel(token, userID, chatID)
		}
		if err != nil && result.Kind == "" {
			result = Result{Kind: "question", Text: "Операция не завершена. Проверьте GitHub и повторите нажатие позже; неподтверждённое создание автоматически не повторяется."}
		}
		return SendResult(ctx, telegram, chatID, callback.Message.ID, result)
	}
	if update.Message == nil {
		return nil
	}
	request := ExtractRequest(*update.Message, username)
	if request == nil {
		return nil
	}
	if _, allowed := service.Allowed(request.UserID, request.ChatID); !allowed {
		return nil
	}
	if err := telegram.Call(ctx, "sendMessage", map[string]any{"chat_id": request.ChatID, "text": "Проверю проект и похожие задачи.", "reply_parameters": map[string]int64{"message_id": request.MessageID}}, nil); err != nil {
		return err
	}
	result, err := service.Prepare(ctx, *request)
	if err != nil {
		log.Print("Preparation failed; no issue creation attempted")
		result = Result{Kind: "question", Text: "Не удалось подготовить задачу. Проверьте доступ к GitHub и модели, укажите разрешённый owner/repo и повторите команду."}
	}
	return SendResult(ctx, telegram, request.ChatID, request.MessageID, result)
}
func RunBot(ctx context.Context, service *Service, telegram Telegram) error {
	var identity struct {
		Username string `json:"username"`
	}
	if err := telegram.Call(ctx, "getMe", map[string]any{}, &identity); err != nil {
		return fmt.Errorf("Telegram identity unavailable")
	}
	if identity.Username == "" {
		return fmt.Errorf("Telegram bot username missing")
	}
	for ctx.Err() == nil {
		var updates []Update
		err := telegram.Call(ctx, "getUpdates", map[string]any{"offset": service.Store.Offset(), "timeout": 30, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		if err != nil {
			log.Print("Polling unavailable; retrying")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}
		for _, update := range updates {
			if err := HandleUpdate(ctx, update, service, telegram, identity.Username); err != nil {
				log.Printf("Update failed or delivery uncertain; update_id=%d", update.ID)
			}
			// Never blindly replay uncertain Telegram sends. Drafts and results are durable.
			if err := service.Store.SetOffset(update.ID + 1); err != nil {
				return fmt.Errorf("cannot persist polling offset: %w", err)
			}
		}
	}
	return ctx.Err()
}
