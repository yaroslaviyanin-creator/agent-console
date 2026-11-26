package provider

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const testSessionID = "019abcde-0123-4567-89ab-0123456789ab"

func TestParseCodexJSONL(t *testing.T) {
	for _, tc := range []struct {
		file     string
		events   []Event
		terminal string
	}{
		{"codex-complete", []Event{
			{Type: Thinking, Delta: "Checking"},
			{Type: Thinking, Delta: " the context."},
			{Type: Text, Delta: "Hello, world."},
		}, Done},
		{"codex-failed", nil, Error},
	} {
		t.Run(tc.file, func(t *testing.T) {
			file, err := os.Open("testdata/" + tc.file + ".jsonl")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			var events []Event
			result, err := parseJSONL(file, func(e Event) bool { events = append(events, e); return true })
			if err != nil || result.Type != tc.terminal || !reflect.DeepEqual(events, tc.events) {
				t.Fatalf("events=%#v terminal=%#v error=%v", events, result, err)
			}
			if tc.terminal == Done && (result.SessionID != testSessionID || result.Usage == nil || *result.Usage != (Usage{InputTokens: 10, CachedInputTokens: 4, OutputTokens: 3})) {
				t.Fatalf("resume ID or usage missing: %+v", result)
			}
			if tc.terminal == Error && result.Err == nil {
				t.Fatal("missing provider error")
			}
		})
	}
}

func TestParseSessionIDVariantAndInvalidStreams(t *testing.T) {
	const alternate = `{"type":"session.started","session_id":"` + testSessionID + `"}
{"type":"item.completed","item":{"type":"agent_message","id":"answer","text":"Ответ"}}
{"type":"turn.completed"}
`
	accept := func(Event) bool { return true }
	event, err := parseJSONL(strings.NewReader(alternate), accept)
	if err != nil || event.SessionID != testSessionID {
		t.Fatalf("session_id variant: %#v %v", event, err)
	}
	for _, data := range []string{"invalid JSON", strings.Split(alternate, "{\"type\":\"turn.completed\"}")[0], strings.ReplaceAll(alternate, testSessionID, "--last")} {
		if _, err := parseJSONL(strings.NewReader(data), accept); err == nil {
			t.Fatal("accepted malformed or truncated stream")
		}
	}
}

func TestCodexArgs(t *testing.T) {
	args, err := codexArgs(Request{Model: "default", Prompt: "--dangerously-bypass-approvals-and-sandbox"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slices.Contains(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("prompt leaked into args: %q", args)
	}
	expected := []string{"exec", "--strict-config", "--ignore-rules", "--skip-git-repo-check", "--json", "-"}
	if !slices.Equal(args, expected) {
		t.Fatalf("args mismatch:\nexpected: %q\ngot:      %q", expected, args)
	}

	args, err = codexArgs(Request{
		Model:              "test-model",
		Reasoning:          "High",
		SystemInstructions: "Only chat",
		Ephemeral:          true,
		SessionID:          testSessionID,
	})
	if err != nil {
		t.Fatalf("unexpected resume error: %v", err)
	}
	for _, want := range []string{
		"--strict-config",
		"--ignore-rules",
		`developer_instructions="Only chat"`,
		"--ephemeral",
		"--model=test-model",
		`model_reasoning_effort="high"`,
	} {
		if !slices.Contains(args, want) {
			t.Fatalf("expected %q in args: %q", want, args)
		}
	}
	if !slices.Equal(args[len(args)-3:], []string{"resume", testSessionID, "-"}) {
		t.Fatalf("incorrect resume arguments: %q", args)
	}

	if _, err := codexArgs(Request{SessionID: "--last"}); err == nil {
		t.Fatal("accepted an option as a session ID")
	}
	if _, err := codexArgs(Request{Reasoning: "invalid"}); err == nil {
		t.Fatal("accepted an invalid reasoning effort")
	}
}
