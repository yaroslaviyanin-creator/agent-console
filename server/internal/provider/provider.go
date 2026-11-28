package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrUnsupportedImageAttachments = errors.New("provider does not support image attachments in safe chat mode")

type ImageAttachment struct {
	Name     string
	MIMEType string
	Data     []byte
}

type Request struct {
	UserID             string
	Model              string
	Reasoning          string
	Prompt             string
	SessionID          string
	SystemInstructions string
	Ephemeral          bool
	Images             []ImageAttachment
}

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens,omitempty"`
}

type Event struct {
	Usage     *Usage
	Type      string
	Delta     string
	SessionID string
	Err       error
}

const (
	Thinking = "thinking_delta"
	Text     = "text_delta"
	Done     = "done"
	Error    = "error"
)

type Provider interface {
	Stream(context.Context, Request) (<-chan Event, error)
}

func send(ctx context.Context, out chan<- Event, event Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

type StubProvider struct {
	Delay time.Duration
}

func (p StubProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	out := make(chan Event)
	go func() {
		defer close(out)
		if req.Ephemeral {
			send(ctx, out, Event{Type: Done})
			return
		}
		imageNote := ""
		if len(req.Images) > 0 {
			imageNote = fmt.Sprintf(" [изображений: %d]", len(req.Images))
		}
		for _, event := range []Event{
			{Type: Thinking, Delta: "Готовлю демо-ответ."},
			{Type: Text, Delta: "Получен запрос: "},
			{Type: Text, Delta: req.Prompt + imageNote},
			{Type: Text, Delta: "\n\nОтвет от StubProvider."},
			{Type: Done, Usage: &Usage{InputTokens: max(1, int64(len(req.Prompt)/4)), OutputTokens: 40}},
		} {
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.Delay):
				if !send(ctx, out, event) {
					return
				}
			}
		}
	}()
	return out, nil
}
