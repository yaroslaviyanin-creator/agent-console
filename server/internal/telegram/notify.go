package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const DefaultBotAPIBaseURL = "https://api.telegram.org"

var consoleFileTagRe = regexp.MustCompile(`(?s)<console_file\b[^>]*>.*?</console_file>`)

func FormatReplyNotification(chatTitle, modelLabel, text string, attachmentsCount int, isError bool) string {
	title := strings.TrimSpace(chatTitle)
	if title == "" {
		title = "Чат"
	}
	model := strings.TrimSpace(modelLabel)
	if model == "" {
		model = "ассистент"
	}

	var b strings.Builder
	if isError {
		b.WriteString(fmt.Sprintf("⚠️ Ошибка ответа в чате «%s» (%s)\n", title, model))
	} else {
		b.WriteString(fmt.Sprintf("✅ Ответ готов в чате «%s» (%s)\n", title, model))
	}

	cleaned := strings.TrimSpace(consoleFileTagRe.ReplaceAllString(text, ""))
	if cleaned != "" {
		runes := []rune(cleaned)
		if len(runes) > 300 {
			cleaned = strings.TrimSpace(string(runes[:300])) + "…"
		}
		b.WriteString("\n")
		b.WriteString(cleaned)
	}
	if attachmentsCount > 0 {
		b.WriteString(fmt.Sprintf("\n\n📎 Файлов: %d", attachmentsCount))
	}
	return b.String()
}

func SendNotification(ctx context.Context, client *http.Client, apiBaseURL, botToken, telegramUserID, text, openChatURL string) error {
	if strings.TrimSpace(botToken) == "" || strings.TrimSpace(telegramUserID) == "" {
		return errors.New("missing telegram bot token or user id")
	}
	if apiBaseURL == "" {
		apiBaseURL = DefaultBotAPIBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	payload := map[string]any{
		"chat_id": telegramUserID,
		"text":    text,
	}
	if strings.HasPrefix(openChatURL, "https://") {
		payload["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]any{
				{
					{
						"text":    "Открыть чат",
						"web_app": map[string]string{"url": openChatURL},
					},
				},
			},
		}
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(apiBaseURL, "/") + "/bot" + botToken + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram sendMessage HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
