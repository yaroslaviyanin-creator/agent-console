package provider

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"
)

type IndexedChat struct {
	ID        string
	Title     string
	Model     string
	CreatedAt int64
	UpdatedAt int64
	Archived  bool
}

type IndexedTurn struct {
	ID          string
	Prompt      string
	Attachments []IndexedAttachment
	Answer      string
	Progress    string
	DurationMs  *int64
	CreatedAt   int64
}

type IndexedAttachment struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type appPage[T any] struct {
	Data       []T    `json:"data"`
	NextCursor string `json:"nextCursor"`
}

func appPages[T any](rpc appRPC, method string, params map[string]any, visit func(T)) error {
	seen := make(map[string]bool)
	for {
		result, err := rpc(method, params)
		if err != nil {
			return err
		}
		var page appPage[T]
		if err := json.Unmarshal(result, &page); err != nil {
			return err
		}
		if page.Data == nil {
			return errors.New("missing app-server page data")
		}
		for _, item := range page.Data {
			visit(item)
		}
		if page.NextCursor == "" {
			return nil
		}
		if seen[page.NextCursor] {
			return errors.New("repeated app-server cursor")
		}
		seen[page.NextCursor] = true
		params["cursor"] = page.NextCursor
	}
}

type indexedThread struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Preview   string `json:"preview"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

func (p CodexCliProvider) ListIndexedChats(ctx context.Context) ([]IndexedChat, error) {
	chats := []IndexedChat{}
	seen := make(map[string]bool)
	err := p.withAppServer(ctx, 8*time.Second, func(rpc appRPC) error {
		for _, archived := range []bool{false, true} {
			params := map[string]any{
				"limit": 100, "sortKey": "updated_at", "modelProviders": []string{}, "archived": archived,
				"sourceKinds": []string{"cli", "vscode", "appServer", "exec", "unknown"},
			}
			if err := appPages(rpc, "thread/list", params, func(thread indexedThread) {
				if !sessionPattern.MatchString(thread.ID) || seen[thread.ID] {
					return
				}
				seen[thread.ID] = true
				title := strings.TrimSpace(thread.Name)
				if title == "" {
					title = strings.TrimSpace(thread.Preview)
				}
				if title == "" {
					title = "Чат Codex"
				}
				if chars := []rune(title); len(chars) > 160 {
					title = string(chars[:160])
				}
				chats = append(chats, IndexedChat{ID: thread.ID, Title: title, Model: thread.Model, CreatedAt: thread.CreatedAt * 1000, UpdatedAt: thread.UpdatedAt * 1000, Archived: archived})
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return chats, err
}

type indexedTurn struct {
	ID          string `json:"id"`
	StartedAt   int64  `json:"startedAt"`
	CompletedAt int64  `json:"completedAt"`
	DurationMs  *int64 `json:"durationMs"`
	Status      string `json:"status"`
	Items       []struct {
		Type    string `json:"type"`
		Phase   string `json:"phase"`
		Text    string `json:"text"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Path string `json:"path"`
		} `json:"content"`
	} `json:"items"`
}

func (p CodexCliProvider) ReadIndexedTurns(ctx context.Context, id string) ([]IndexedTurn, error) {
	if !sessionPattern.MatchString(id) {
		return nil, errors.New("invalid indexed chat ID")
	}
	turns := []IndexedTurn{}
	err := p.withAppServer(ctx, 8*time.Second, func(rpc appRPC) error {
		params := map[string]any{"threadId": id, "itemsView": "full", "sortDirection": "asc", "limit": 20}
		return appPages(rpc, "thread/turns/list", params, func(turn indexedTurn) {
			mapped := splitIndexedTurn(turn)
			if mapped.Prompt != "" || len(mapped.Attachments) > 0 || mapped.Answer != "" || mapped.Progress != "" {
				turns = append(turns, mapped)
			}
		})
	})
	return turns, err
}

var browserCompletion = regexp.MustCompile(`^::browser-complete\{title="[^"\r\n]*"\}$`)

var (
	filesHeader   = regexp.MustCompile(`^# Files mentioned by the user:[ \t]*\r?\n`)
	requestHeader = regexp.MustCompile(`(?m)^## My request:[ \t]*\r?(?:\n|$)`)
	fileHeading   = regexp.MustCompile(`^(?:## |- )(.+?):[ \t]+(.+)$`)
	fileLink      = regexp.MustCompile(`^(?:- |\* )?\[([^\]]+)\]\((.+)\)$`)
)

func splitAttachmentPrompt(text string) (string, []IndexedAttachment) {
	header := filesHeader.FindStringIndex(text)
	if header == nil {
		return text, nil
	}
	body := text[header[1]:]
	request := requestHeader.FindStringIndex(body)
	if request == nil {
		return text, nil
	}
	var attachments []IndexedAttachment
	for _, line := range strings.Split(body[:request[0]], "\n") {
		line = strings.TrimSpace(line)
		entry := fileHeading.FindStringSubmatch(line)
		if entry == nil {
			entry = fileLink.FindStringSubmatch(line)
		}
		if entry == nil {
			continue
		}
		location := strings.Trim(strings.TrimSpace(entry[2]), "<>`")
		normalized := strings.ReplaceAll(location, "\\", "/")
		if path.IsAbs(normalized) || (len(normalized) >= 3 && normalized[1:3] == ":/") {
			attachments = append(attachments, IndexedAttachment{Kind: "file", Name: strings.TrimSpace(entry[1]), Path: location})
		}
	}
	return strings.TrimLeft(body[request[1]:], "\r\n"), attachments
}

func splitIndexedTurn(turn indexedTurn) IndexedTurn {
	type message struct{ text, phase string }
	var prompts []string
	var attachments []IndexedAttachment
	attachmentPaths := make(map[string]int)
	addAttachment := func(attachment IndexedAttachment) {
		if attachment.Path != "" {
			if index, ok := attachmentPaths[attachment.Path]; ok {
				if attachment.Kind == "image" {
					attachments[index].Kind = "image"
				} else {
					attachments[index].Name = attachment.Name
				}
				return
			}
			attachmentPaths[attachment.Path] = len(attachments)
		}
		attachments = append(attachments, attachment)
	}
	var messages []message
	hasFinal := false
	lastUnlabelled := -1
	for _, item := range turn.Items {
		switch item.Type {
		case "userMessage":
			for _, part := range item.Content {
				switch part.Type {
				case "text":
					prompt, files := splitAttachmentPrompt(part.Text)
					if prompt != "" {
						prompts = append(prompts, prompt)
					}
					for _, file := range files {
						addAttachment(file)
					}
				case "localImage":
					if part.Path != "" {
						addAttachment(IndexedAttachment{Kind: "image", Name: path.Base(strings.ReplaceAll(part.Path, "\\", "/")), Path: part.Path})
					}
				case "image":
					addAttachment(IndexedAttachment{Kind: "image", Name: "Изображение"})
				}
			}
		case "agentMessage":
			text := strings.TrimSpace(item.Text)
			if text == "" || (item.Phase != "final_answer" && browserCompletion.MatchString(text)) {
				continue
			}
			if item.Phase == "final_answer" {
				hasFinal = true
			}
			if item.Phase == "" {
				lastUnlabelled = len(messages)
			}
			messages = append(messages, message{text: item.Text, phase: item.Phase})
		}
	}
	var final, progress []string
	for i, message := range messages {
		// Legacy completed turns can omit phases. Only their trailing unlabelled
		// item can stand in for a final answer.
		if message.phase == "final_answer" || (!hasFinal && turn.Status == "completed" &&
			i == lastUnlabelled && i == len(messages)-1) {
			final = append(final, message.text)
		} else {
			progress = append(progress, message.text)
		}
	}
	var duration *int64
	if turn.Status == "completed" {
		if turn.DurationMs != nil && *turn.DurationMs >= 0 {
			duration = turn.DurationMs
		} else if turn.StartedAt > 0 && turn.CompletedAt >= turn.StartedAt {
			elapsed := (turn.CompletedAt - turn.StartedAt) * 1000
			duration = &elapsed
		}
	}
	return IndexedTurn{
		ID: turn.ID, Prompt: strings.Join(prompts, "\n\n"), Answer: strings.Join(final, "\n\n"),
		Attachments: attachments,
		Progress:    strings.Join(progress, "\n\n"), DurationMs: duration, CreatedAt: turn.StartedAt * 1000,
	}
}
