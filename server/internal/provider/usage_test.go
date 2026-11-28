package provider

import (
	"strings"
	"testing"
)

func TestParseUsageReasoningTokensAcrossProviders(t *testing.T) {
	codexJSONL := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019a0000-0000-7000-8000-000000000001"}`,
		`{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"ok"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":14200,"cached_input_tokens":8000,"output_tokens":420,"output_tokens_details":{"reasoning_tokens":1200}}}`,
	}, "\n")
	ev, err := parseJSONL(strings.NewReader(codexJSONL), func(Event) bool { return true })
	if err != nil {
		t.Fatalf("parseJSONL failed: %v", err)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 14200 || ev.Usage.CachedInputTokens != 8000 || ev.Usage.OutputTokens != 420 || ev.Usage.ReasoningTokens != 1200 {
		t.Fatalf("unexpected codex usage: %+v", ev.Usage)
	}

	agyJSONL := strings.Join([]string{
		`{"event":"init","conversation_id":"conv-1"}`,
		`{"event":"step_update","step_update":{"step_type":"text","text_delta":"done"}}`,
		`{"event":"result","result":{"status":"SUCCESS","usage":{"input_tokens":500,"cache_read_tokens":100,"output_tokens":80,"thoughtsTokenCount":45}}}`,
	}, "\n")
	agyEv, err := parseAgyJSONL(strings.NewReader(agyJSONL), func(Event) bool { return true })
	if err != nil {
		t.Fatalf("parseAgyJSONL failed: %v", err)
	}
	if agyEv.Usage == nil || agyEv.Usage.InputTokens != 500 || agyEv.Usage.CachedInputTokens != 100 || agyEv.Usage.OutputTokens != 80 || agyEv.Usage.ReasoningTokens != 45 {
		t.Fatalf("unexpected agy usage: %+v", agyEv.Usage)
	}

	claudeJSONL := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"019a0000-0000-7000-8000-000000000002"}`,
		`{"type":"result","subtype":"success","session_id":"019a0000-0000-7000-8000-000000000002","result":"hello","usage":{"input_tokens":200,"cache_read_input_tokens":50,"cache_creation_input_tokens":25,"output_tokens":40}}`,
	}, "\n")
	claudeEv, err := parseClaudeJSONL(strings.NewReader(claudeJSONL), func(Event) bool { return true })
	if err != nil {
		t.Fatalf("parseClaudeStreamJSON failed: %v", err)
	}
	if claudeEv.Usage == nil || claudeEv.Usage.InputTokens != 275 || claudeEv.Usage.CachedInputTokens != 50 || claudeEv.Usage.OutputTokens != 40 || claudeEv.Usage.ReasoningTokens != 0 {
		t.Fatalf("unexpected claude usage: %+v", claudeEv.Usage)
	}
}
