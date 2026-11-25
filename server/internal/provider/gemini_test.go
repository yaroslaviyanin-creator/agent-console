package provider

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testGeminiSessionID = "aa5b5f7f-5aba-4a44-9ea1-798f6f071849"

func TestGeminiArgs(t *testing.T) {
	// 1. New session with default model
	args, err := geminiArgs(Request{Prompt: "Hello world"}, "gemini-3.8-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"--skip-trust", "--approval-mode", "plan", "-o", "stream-json", "-m", "gemini-3.8-flash", "-p", "Hello world"}
	if !slices.Equal(args, expected) {
		t.Fatalf("args mismatch:\nexpected: %v\ngot:      %v", expected, args)
	}

	// 2. Custom model
	args, err = geminiArgs(Request{Model: "gemini-2.5-pro", Prompt: "Hi"}, "gemini-3.8-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(args, "gemini-2.5-pro") {
		t.Fatalf("expected custom model in args: %v", args)
	}

	// 3. Resume session
	args, err = geminiArgs(Request{SessionID: testGeminiSessionID, Prompt: "Continue"}, "gemini-3.8-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(args, "-r") || !slices.Contains(args, testGeminiSessionID) {
		t.Fatalf("expected resume flag and session id in args: %v", args)
	}

	// 4. Invalid session IDs (flag injection or non-UUID)
	for _, badID := range []string{"-r", "--resume", "invalid-id", "../relative", "123"} {
		if _, err := geminiArgs(Request{SessionID: badID, Prompt: "test"}, "gemini-3.8-flash"); err == nil {
			t.Fatalf("expected error for bad session id %q, got nil", badID)
		}
	}

	// 5. Invalid model (flag injection)
	if _, err := geminiArgs(Request{Model: "--danger-mode", Prompt: "test"}, "gemini-3.8-flash"); err == nil {
		t.Fatalf("expected error for flag-like model name, got nil")
	}
}

func TestParseGeminiJSONL_Success(t *testing.T) {
	fixture := `Warning: 256-color support not detected.
Ripgrep is not available. Falling back to GrepTool.
{"type":"init","timestamp":"2026-11-25T19:01:05.299Z","session_id":"` + testGeminiSessionID + `","model":"gemini-3.8-flash"}
{"type":"message","timestamp":"2026-11-25T19:01:05.301Z","role":"user","content":"hello"}
{"type":"thought","content":"Thinking step 1"}
{"type":"message","timestamp":"2026-11-25T19:01:05.302Z","role":"assistant","content":"Hello","delta":true}
{"type":"message","timestamp":"2026-11-25T19:01:05.303Z","role":"assistant","content":" there!","delta":true}
{"type":"result","timestamp":"2026-11-25T19:01:07.713Z","status":"success","stats":{"total_tokens":55,"input_tokens":15,"output_tokens":40,"cached":5,"duration_ms":120}}
`
	var events []Event
	terminal, err := parseGeminiJSONL(strings.NewReader(fixture), func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedEvents := []Event{
		{Type: Thinking, Delta: "Thinking step 1"},
		{Type: Text, Delta: "Hello"},
		{Type: Text, Delta: " there!"},
	}
	if !reflect.DeepEqual(events, expectedEvents) {
		t.Fatalf("events mismatch:\nexpected: %#v\ngot:      %#v", expectedEvents, events)
	}
	if terminal.Type != Done {
		t.Fatalf("expected terminal Type Done, got %q", terminal.Type)
	}
	if terminal.SessionID != testGeminiSessionID {
		t.Fatalf("expected session ID %q, got %q", testGeminiSessionID, terminal.SessionID)
	}
	if terminal.Usage == nil {
		t.Fatal("expected usage, got nil")
	}
	expectedUsage := Usage{
		InputTokens:       15,
		CachedInputTokens: 5,
		OutputTokens:      40,
	}
	if *terminal.Usage != expectedUsage {
		t.Fatalf("usage mismatch:\nexpected: %+v\ngot:      %+v", expectedUsage, *terminal.Usage)
	}
}

func TestParseGeminiJSONL_Error(t *testing.T) {
	fixture := `{"type":"init","session_id":"` + testGeminiSessionID + `"}
{"type":"result","status":"error","error":{"type":"Error","message":"API key not valid"}}
`
	terminal, err := parseGeminiJSONL(strings.NewReader(fixture), func(e Event) bool { return true })
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if terminal.Type != Error {
		t.Fatalf("expected terminal Type Error, got %q", terminal.Type)
	}
	if terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "API key not valid") {
		t.Fatalf("expected error with message 'API key not valid', got: %v", terminal.Err)
	}

	// Truncated / empty stream
	if _, err := parseGeminiJSONL(strings.NewReader(`{"type":"init"}`), func(e Event) bool { return true }); err == nil {
		t.Fatal("expected error on truncated stream without message or result")
	}
}

func TestGeminiListModels(t *testing.T) {
	ctx := t.Context()
	p := GeminiCliProvider{DefaultModel: "gemini-3.8-flash"}
	models, err := p.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(models) == 0 {
		t.Fatal("expected at least one model")
	}
	if models[0].ID != "gemini-3.8-flash" || !models[0].IsDefault {
		t.Fatalf("expected first model to be default gemini-3.8-flash, got: %+v", models[0])
	}

	// Test override with GEMINI_MODELS
	t.Setenv("GEMINI_MODELS", "custom-1:Custom One,custom-2")
	overrideProvider := GeminiCliProvider{DefaultModel: "custom-1"}
	overridden, err := overrideProvider.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels with override failed: %v", err)
	}
	if len(overridden) != 2 || overridden[0].ID != "custom-1" || !overridden[0].IsDefault || overridden[1].Name != "custom-2" {
		t.Fatalf("unexpected overridden models: %+v", overridden)
	}
}
