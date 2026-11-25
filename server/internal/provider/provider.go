package provider

import (
	"context"
	"time"
)

type Request struct {
	UserID             string
	Model              string
	Prompt             string
	SessionID          string
	SystemInstructions string
	Ephemeral          bool
}

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
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
		for _, event := range []Event{
			{Type: Thinking, Delta: "Готовлю демо-ответ."},
			{Type: Text, Delta: "Получен запрос: "},
			{Type: Text, Delta: req.Prompt},
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
