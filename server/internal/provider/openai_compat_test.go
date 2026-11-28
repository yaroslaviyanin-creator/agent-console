package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticResolver struct {
	ep     OpenAICompatEndpoint
	models []Model
}

func (r staticResolver) ResolveModel(context.Context, string) (OpenAICompatEndpoint, error) {
	return r.ep, nil
}

func (r staticResolver) ListModels(context.Context) ([]Model, error) {
	return r.models, nil
}

func TestOpenAICompatStreamSSEAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Думаю... \"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Привет! <console_file name=\\\"hi.txt\\\">\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello</console_file>\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":25,\"prompt_tokens_details\":{\"cached_tokens\":40},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICompatProvider(staticResolver{
		ep: OpenAICompatEndpoint{
			BaseURL: srv.URL,
			APIKey:  "sk-test-secret",
			Model:   OpenAICompatModelSpec{ID: "deepseek-reasoner", Label: "DeepSeek R1", SupportsReasoning: true},
		},
	})
	ch, err := p.Stream(t.Context(), Request{
		Model:              "deepseek-reasoner",
		Prompt:             "Say hi",
		SystemInstructions: "Be concise",
		History:            []HistoryMessage{{Role: "user", Content: "prev"}, {Role: "assistant", Content: "ok"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var thinking, text strings.Builder
	var done Event
	for ev := range ch {
		switch ev.Type {
		case Thinking:
			thinking.WriteString(ev.Delta)
		case Text:
			text.WriteString(ev.Delta)
		case Done:
			done = ev
		case Error:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if thinking.String() != "Думаю... " {
		t.Fatalf("unexpected thinking: %q", thinking.String())
	}
	if text.String() != "Привет! <console_file name=\"hi.txt\">hello</console_file>" {
		t.Fatalf("unexpected text: %q", text.String())
	}
	if done.Type != Done || done.Usage == nil {
		t.Fatalf("expected Done with usage, got %+v", done)
	}
	if done.Usage.InputTokens != 100 || done.Usage.OutputTokens != 25 || done.Usage.CachedInputTokens != 40 || done.Usage.ReasoningTokens != 10 {
		t.Fatalf("unexpected usage: %+v", done.Usage)
	}
}
