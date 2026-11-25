package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testAgySessionID = "834a3b51-377b-4b32-b712-b0b6e81e8377"

func TestAntigravityArgs(t *testing.T) {
	// 1. New session with default model
	args, err := antigravityArgs(Request{Prompt: "Hello agy"}, "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []string{"--settings", agyChatOnlySettings, "--output-format", "stream-json", "--model", "gemini-3.8-flash-high", "--print", "Hello agy"}
	if !slices.Equal(args, expected) {
		t.Fatalf("args mismatch:\nexpected: %v\ngot:      %v", expected, args)
	}

	// 2. Custom model
	args, err = antigravityArgs(Request{Model: "claude-sonnet-4-6", Prompt: "Hi"}, "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(args, "claude-sonnet-4-6") {
		t.Fatalf("expected custom model in args: %v", args)
	}

	// 3. Resume session
	args, err = antigravityArgs(Request{SessionID: testAgySessionID, Prompt: "Continue"}, "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Contains(args, "--conversation") || !slices.Contains(args, testAgySessionID) {
		t.Fatalf("expected resume flag and session id in args: %v", args)
	}

	// 4. Invalid session IDs (flag injection or bad chars)
	for _, badID := range []string{"--conversation", "-c", "invalid/path", "../relative"} {
		if _, err := antigravityArgs(Request{SessionID: badID, Prompt: "test"}, "gemini-3.8-flash-high"); err == nil {
			t.Fatalf("expected error for bad session id %q, got nil", badID)
		}
	}

	// 5. Invalid model (flag injection)
	if _, err := antigravityArgs(Request{Model: "--skip-permissions", Prompt: "test"}, "gemini-3.8-flash-high"); err == nil {
		t.Fatalf("expected error for flag-like model name, got nil")
	}
}

func TestParseAgyJSONL_Success(t *testing.T) {
	fixture := `{"event":"init","conversation_id":"` + testAgySessionID + `","init":{"model":"gemini-3.8-flash-high"}}
{"event":"step_update","step_update":{"conversation_id":"` + testAgySessionID + `","step_index":0,"state":"DONE","step_type":"user_input"}}
{"event":"step_update","step_update":{"conversation_id":"` + testAgySessionID + `","step_index":1,"state":"ACTIVE","step_type":"thought","text_delta":"Thinking carefully"}}
{"event":"step_update","step_update":{"conversation_id":"` + testAgySessionID + `","step_index":1,"state":"ACTIVE","step_type":"agent_response","text_delta":"Hello"}}
{"event":"step_update","step_update":{"conversation_id":"` + testAgySessionID + `","step_index":1,"state":"DONE","step_type":"agent_response","text_delta":" world!\n","usage":{"input_tokens":1200,"output_tokens":35,"thinking_tokens":10,"cache_read_tokens":500,"total_tokens":1745}}}
{"event":"result","result":{"conversation_id":"` + testAgySessionID + `","status":"SUCCESS","response":"Hello world!\n","duration_seconds":1.5,"num_turns":1,"usage":{"input_tokens":1200,"output_tokens":35,"thinking_tokens":10,"cache_read_tokens":500,"total_tokens":1745}}}
`
	var events []Event
	terminal, err := parseAgyJSONL(strings.NewReader(fixture), func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedEvents := []Event{
		{Type: Thinking, Delta: "Thinking carefully"},
		{Type: Text, Delta: "Hello"},
		{Type: Text, Delta: " world!\n"},
	}
	if !reflect.DeepEqual(events, expectedEvents) {
		t.Fatalf("events mismatch:\nexpected: %#v\ngot:      %#v", expectedEvents, events)
	}
	if terminal.Type != Done {
		t.Fatalf("expected terminal Type Done, got %q", terminal.Type)
	}
	if terminal.SessionID != testAgySessionID {
		t.Fatalf("expected session ID %q, got %q", testAgySessionID, terminal.SessionID)
	}
	if terminal.Usage == nil {
		t.Fatal("expected usage, got nil")
	}
	expectedUsage := Usage{
		InputTokens:       1200,
		CachedInputTokens: 500,
		OutputTokens:      35,
	}
	if *terminal.Usage != expectedUsage {
		t.Fatalf("usage mismatch:\nexpected: %+v\ngot:      %+v", expectedUsage, *terminal.Usage)
	}
}

func TestParseAgyJSONL_Error(t *testing.T) {
	fixture := `{"event":"init","conversation_id":"` + testAgySessionID + `"}
{"event":"result","result":{"conversation_id":"","status":"ERROR","response":"","error":"model nonexistent is not recognized","duration_seconds":0,"num_turns":0,"usage":{"input_tokens":0,"output_tokens":0,"thinking_tokens":0,"cache_read_tokens":0,"total_tokens":0},"finished_reason":"error"}}
`
	var events []Event
	terminal, err := parseAgyJSONL(strings.NewReader(fixture), func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if terminal.Type != Error {
		t.Fatalf("expected terminal Type Error, got %q", terminal.Type)
	}
	if terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "model nonexistent is not recognized") {
		t.Fatalf("unexpected terminal error: %v", terminal.Err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events, got %v", events)
	}
}

func TestParseAgyModelsOutput(t *testing.T) {
	raw := `Fetching available models...
gemini-4-hydrogen-high	Gemini 4 Hydrogen (High)
gemini-3.8-flash-high	Gemini 3.8 Flash (High)
claude-sonnet-4-6	Claude Sonnet 4.6 (Thinking)
`
	models := parseAgyModelsOutput(raw, "gemini-3.8-flash-high")
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
	if models[0].ID != "gemini-4-hydrogen-high" || models[0].IsDefault {
		t.Fatalf("model 0 mismatch: %+v", models[0])
	}
	if models[1].ID != "gemini-3.8-flash-high" || !models[1].IsDefault {
		t.Fatalf("model 1 should be default: %+v", models[1])
	}
	if models[2].ID != "claude-sonnet-4-6" || models[2].IsDefault {
		t.Fatalf("model 2 mismatch: %+v", models[2])
	}
}

func TestParseAgyJSONL_DeniedActions(t *testing.T) {
	fixture := `{"event":"init","conversation_id":"` + testAgySessionID + `"}
{"event":"result","result":{"conversation_id":"` + testAgySessionID + `","status":"SUCCESS","response":"","denied_actions":[{"action":"command","display_name":"Bash"}]}}
`
	terminal, err := parseAgyJSONL(strings.NewReader(fixture), func(e Event) bool {
		return true
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if terminal.Type != Error {
		t.Fatalf("expected terminal Type Error, got %q", terminal.Type)
	}
	if terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "blocked tool action") {
		t.Fatalf("unexpected terminal error: %v", terminal.Err)
	}
}

func TestWriteAntigravityInstructionsRemovesStaleGeminiMD(t *testing.T) {
	dir := t.TempDir()
	geminiPath := filepath.Join(dir, "GEMINI.md")
	agentsPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(geminiPath, []byte("Old name"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeAntigravityInstructions(dir, "New name: Ivan"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(geminiPath); !os.IsNotExist(err) {
		t.Fatalf("expected stale GEMINI.md to be removed, stat err: %v", err)
	}
	got, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), agyChatOnlyInstructions) || !strings.Contains(string(got), "New name: Ivan") {
		t.Fatalf("unexpected AGENTS.md content: %q", string(got))
	}

	if err := writeAntigravityInstructions(dir, ""); err != nil {
		t.Fatal(err)
	}
	gotEmpty, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotEmpty) != agyChatOnlyInstructions {
		t.Fatalf("expected AGENTS.md to contain only chat-only instructions when empty, got: %q", string(gotEmpty))
	}
}
