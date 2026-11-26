package provider

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const testClaudeSessionID = "550e8400-e29b-41d4-a716-446655440000"

func assertArgPair(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Fatalf("expected %q %q in args: %q", flag, value, args)
}

func TestClaudeArgs(t *testing.T) {
	args, err := claudeArgs(Request{
		Model:  "default",
		Prompt: "--dangerously-skip-permissions",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slices.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("prompt leaked into args: %q", args)
	}
	if slices.Contains(args, "--bare") {
		t.Fatalf("--bare must not be used (disables OAuth): %q", args)
	}
	for _, single := range []string{
		"-p",
		"--verbose",
		"--include-partial-messages",
		"--strict-mcp-config",
		"--restricted",
		"--safe-mode",
		"--disable-slash-commands",
		"--no-chrome",
	} {
		if !slices.Contains(args, single) {
			t.Fatalf("expected %q in args: %q", single, args)
		}
	}
	assertArgPair(t, args, "--output-format", "stream-json")
	assertArgPair(t, args, "--tools", "")
	assertArgPair(t, args, "--mcp-config", `{"mcpServers":{}}`)
	assertArgPair(t, args, "--setting-sources", "")
	assertArgPair(t, args, "--permission-mode", "dontAsk")
	assertArgPair(t, args, "--permission-prompts", "none")
	assertArgPair(t, args, "--system-prompt-snapshot", "off")
	assertArgPair(t, args, "--model", "sonnet")
	assertArgPair(t, args, "--append-system-prompt", claudeChatInstructions)
	if slices.Contains(args, "--no-session-persistence") || slices.Contains(args, "--resume") {
		t.Fatalf("unexpected session persistence flags in default args: %q", args)
	}

	args, err = claudeArgs(Request{
		Model:              "opus",
		SystemInstructions: "Reply concisely.",
		SessionID:          testClaudeSessionID,
	})
	if err != nil {
		t.Fatalf("unexpected resume error: %v", err)
	}
	assertArgPair(t, args, "--model", "opus")
	assertArgPair(t, args, "--append-system-prompt", claudeChatInstructions+"\n\nReply concisely.")
	assertArgPair(t, args, "--resume", testClaudeSessionID)

	ephemeralArgs, err := claudeArgs(Request{
		Model:     "haiku",
		Ephemeral: true,
	})
	if err != nil {
		t.Fatalf("unexpected ephemeral error: %v", err)
	}
	if !slices.Contains(ephemeralArgs, "--no-session-persistence") {
		t.Fatalf("expected --no-session-persistence in ephemeral args: %q", ephemeralArgs)
	}
	if slices.Contains(ephemeralArgs, "--resume") {
		t.Fatalf("ephemeral args must not include --resume: %q", ephemeralArgs)
	}

	for _, badSession := range []string{"--last", "../secret", "not-a-uuid"} {
		if _, err := claudeArgs(Request{SessionID: badSession}); err == nil {
			t.Fatalf("expected error for invalid session ID %q", badSession)
		}
	}
	if _, err := claudeArgs(Request{Model: "--bad-model"}); err == nil {
		t.Fatal("expected error for flag-like model name")
	}
}

func TestClaudeSessionNamespace(t *testing.T) {
	if got := claudeSessionID(claudeSessionPrefix + testClaudeSessionID); got != testClaudeSessionID {
		t.Fatalf("expected stripped session ID %q, got %q", testClaudeSessionID, got)
	}
	for _, foreign := range []string{
		"",
		testClaudeSessionID,
		chatSessionPrefix + testClaudeSessionID,
		"other-v1:" + testClaudeSessionID,
	} {
		if got := claudeSessionID(foreign); got != "" {
			t.Fatalf("expected empty session ID for foreign/un-namespaced %q, got %q", foreign, got)
		}
	}
	if _, err := claudeArgs(Request{SessionID: claudeSessionID(claudeSessionPrefix + "--last")}); err == nil {
		t.Fatal("expected error for prefixed flag injection")
	}
}

func TestClaudeEnvironmentWhitelist(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("LANG", "C.UTF-8")
	t.Setenv("LC_ALL", "C.UTF-8")
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("HOME", "/some/other/home")
	t.Setenv("CLAUDE_CONFIG_DIR", "/some/other/claude")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret")
	t.Setenv("OPENAI_API_KEY", "sk-openai")
	t.Setenv("RUNNER_SHARED_SECRET", "runner-secret")
	t.Setenv("HTTP_PROXY", "http://proxy:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy:8443")
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("ALL_PROXY", "socks5://proxy:1080")
	t.Setenv("http_proxy", "http://proxy:8080")
	t.Setenv("https_proxy", "http://proxy:8443")
	t.Setenv("DOTENV_LOADED", "true")

	env := claudeEnvironment("/home/app")
	for _, want := range []string{
		"PATH=/usr/local/bin:/usr/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TMPDIR=/tmp",
		"HOME=/home/app",
		"CLAUDE_CONFIG_DIR=" + filepath.Join("/home/app", ".claude"),
	} {
		if !slices.Contains(env, want) {
			t.Fatalf("expected %q in env: %v", want, env)
		}
	}
	for _, forbiddenPrefix := range []string{
		"HOME=/some/other/home",
		"CLAUDE_CONFIG_DIR=/some/other/claude",
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_AUTH_TOKEN=",
		"CLAUDE_CODE_OAUTH_TOKEN=",
		"OPENAI_API_KEY=",
		"RUNNER_SHARED_SECRET=",
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"NO_PROXY=",
		"ALL_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"DOTENV_LOADED=",
	} {
		for _, entry := range env {
			if strings.HasPrefix(entry, forbiddenPrefix) {
				t.Fatalf("forbidden env entry %q leaked into claudeEnvironment: %v", entry, env)
			}
		}
	}
}

func TestClaudeListModels(t *testing.T) {
	models, err := (ClaudeCliProvider{}).ListModels(t.Context())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	want := []Model{
		{ID: "sonnet", Name: "Claude Sonnet", IsDefault: true},
		{ID: "opus", Name: "Claude Opus", IsDefault: false},
		{ID: "haiku", Name: "Claude Haiku", IsDefault: false},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models mismatch:\nwant: %#v\ngot:  %#v", want, models)
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (ClaudeCliProvider{}).ListModels(canceled); err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestParseClaudeJSONL_PartialStreamingNoDuplicates(t *testing.T) {
	fixture := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `","model":"claude-sonnet-4-6"}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","role":"assistant"}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Checking"}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" context."}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello, "}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world!"}},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"thinking","thinking":"Checking context."},{"type":"text","text":"Hello, world!"}]},"session_id":"` + testClaudeSessionID + `"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"Hello, world!","session_id":"` + testClaudeSessionID + `","usage":{"input_tokens":120,"cache_creation_input_tokens":50,"cache_read_input_tokens":340,"output_tokens":19}}`,
	}, "\n") + "\n"

	var events []Event
	terminal, err := parseClaudeJSONL(strings.NewReader(fixture), func(e Event) bool {
		events = append(events, e)
		return true
	})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	wantEvents := []Event{
		{Type: Thinking, Delta: "Checking"},
		{Type: Thinking, Delta: " context."},
		{Type: Text, Delta: "Hello, "},
		{Type: Text, Delta: "world!"},
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events mismatch:\nwant: %#v\ngot:  %#v", wantEvents, events)
	}
	if terminal.Type != Done || terminal.SessionID != testClaudeSessionID {
		t.Fatalf("unexpected terminal event: %#v", terminal)
	}
	wantUsage := Usage{
		InputTokens:       510,
		CachedInputTokens: 340,
		OutputTokens:      19,
	}
	if terminal.Usage == nil || *terminal.Usage != wantUsage {
		t.Fatalf("usage mismatch: want %+v, got %#v", wantUsage, terminal.Usage)
	}
}

func TestParseClaudeJSONL_AssistantAndResultFallbacks(t *testing.T) {
	t.Run("assistant_complete_fallback_without_stream_events", func(t *testing.T) {
		fixture := strings.Join([]string{
			`{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `"}`,
			`{"type":"user","message":{"role":"user","content":"ignored string content"}}`,
			`{"type":"assistant","message":{"id":"msg_a1","content":[{"type":"thinking","thinking":"Reasoning"},{"type":"text","text":"Answer"}]}}`,
			`{"type":"assistant","message":{"id":"msg_a1","content":[{"type":"thinking","thinking":"Reasoning"},{"type":"text","text":"Answer"}]}}`,
			`{"type":"result","subtype":"success","is_error":false,"result":"Answer","session_id":"` + testClaudeSessionID + `"}`,
		}, "\n") + "\n"

		var events []Event
		terminal, err := parseClaudeJSONL(strings.NewReader(fixture), func(e Event) bool {
			events = append(events, e)
			return true
		})
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		want := []Event{
			{Type: Thinking, Delta: "Reasoning"},
			{Type: Text, Delta: "Answer"},
		}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events mismatch:\nwant: %#v\ngot:  %#v", want, events)
		}
		if terminal.Type != Done || terminal.SessionID != testClaudeSessionID {
			t.Fatalf("unexpected terminal: %#v", terminal)
		}
	})

	t.Run("result_only_session_id_and_text_fallback", func(t *testing.T) {
		fixture := `{"type":"result","subtype":"success","is_error":false,"result":"Direct response","session_id":"` + testClaudeSessionID + `","usage":{"input_tokens":5,"cache_read_input_tokens":2,"output_tokens":3}}` + "\n"

		var events []Event
		terminal, err := parseClaudeJSONL(strings.NewReader(fixture), func(e Event) bool {
			events = append(events, e)
			return true
		})
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		want := []Event{{Type: Text, Delta: "Direct response"}}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events mismatch: want %#v, got %#v", want, events)
		}
		if terminal.Type != Done || terminal.SessionID != testClaudeSessionID {
			t.Fatalf("unexpected terminal: %#v", terminal)
		}
		if terminal.Usage == nil || terminal.Usage.CachedInputTokens != 2 {
			t.Fatalf("unexpected usage: %#v", terminal.Usage)
		}
	})
}

func TestParseClaudeJSONL_ErrorsAndNoTerminal(t *testing.T) {
	accept := func(Event) bool { return true }

	t.Run("top_level_error_event", func(t *testing.T) {
		fixture := `{"type":"error","error":{"type":"authentication_error","message":"OAuth token expired"}}` + "\n"
		terminal, err := parseClaudeJSONL(strings.NewReader(fixture), accept)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if terminal.Type != Error || terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "OAuth token expired") {
			t.Fatalf("expected authentication error, got %#v", terminal)
		}
	})

	t.Run("result_error_event", func(t *testing.T) {
		fixture := `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["model unavailable"],"session_id":"` + testClaudeSessionID + `"}` + "\n"
		terminal, err := parseClaudeJSONL(strings.NewReader(fixture), accept)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if terminal.Type != Error || terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "model unavailable") {
			t.Fatalf("expected result error, got %#v", terminal)
		}
	})

	t.Run("assistant_error_string", func(t *testing.T) {
		fixture := `{"type":"assistant","error":"authentication_failed","message":{"id":"msg_e","content":[{"type":"text","text":"Please login"}]}}` + "\n"
		terminal, err := parseClaudeJSONL(strings.NewReader(fixture), accept)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		if terminal.Type != Error || terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "authentication_failed") {
			t.Fatalf("expected assistant error, got %#v", terminal)
		}
	})

	for name, badStream := range map[string]string{
		"no_terminal_result": `{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `"}` + "\n" +
			`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}}` + "\n",
		"empty_response": `{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `"}` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"result":""}` + "\n",
		"missing_session_id": `{"type":"result","subtype":"success","is_error":false,"result":"hello"}` + "\n",
		"invalid_session_id": `{"type":"system","subtype":"init","session_id":"--resume-inject"}` + "\n",
		"invalid_json":       "not json\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseClaudeJSONL(strings.NewReader(badStream), accept); err == nil {
				t.Fatalf("expected parse error for %s", name)
			}
		})
	}
}

func TestClaudeStream_ProcessIntegrationAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell script")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RUNNING_IN_DOCKER", "")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-leak")
	t.Setenv("RUNNER_SHARED_SECRET", "must-not-leak")

	argsRecord := filepath.Join(t.TempDir(), "args.txt")
	stdinRecord := filepath.Join(t.TempDir(), "stdin.txt")
	envRecord := filepath.Join(t.TempDir(), "env.txt")

	fakeBin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "` + argsRecord + `"
cat > "` + stdinRecord + `"
env > "` + envRecord + `"
printf '{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `"}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok"}}}\n'
printf '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"` + testClaudeSessionID + `","usage":{"input_tokens":10,"cache_read_input_tokens":4,"output_tokens":2}}\n'
`
	if err := os.WriteFile(fakeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	provider := ClaudeCliProvider{Bin: fakeBin}
	validUserID := strings.Repeat("a", 32)

	t.Run("stdin_env_and_namespaced_session", func(t *testing.T) {
		ch, err := provider.Stream(t.Context(), Request{
			UserID: validUserID,
			Prompt: "--prompt-via-stdin",
		})
		if err != nil {
			t.Fatalf("Stream failed: %v", err)
		}
		var events []Event
		for ev := range ch {
			events = append(events, ev)
		}
		if len(events) != 2 || events[0] != (Event{Type: Text, Delta: "ok"}) {
			t.Fatalf("unexpected events: %#v", events)
		}
		done := events[1]
		if done.Type != Done || done.SessionID != claudeSessionPrefix+testClaudeSessionID {
			t.Fatalf("expected namespaced session ID, got %#v", done)
		}
		gotStdin, err := os.ReadFile(stdinRecord)
		if err != nil || string(gotStdin) != "--prompt-via-stdin" {
			t.Fatalf("stdin mismatch: %q err=%v", gotStdin, err)
		}
		gotArgs, err := os.ReadFile(argsRecord)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(gotArgs), "--prompt-via-stdin") {
			t.Fatalf("prompt appeared in CLI args: %s", gotArgs)
		}
		gotEnv, err := os.ReadFile(envRecord)
		if err != nil {
			t.Fatal(err)
		}
		envText := string(gotEnv)
		if !strings.Contains(envText, "HOME="+home+"\n") {
			t.Fatalf("expected HOME=%s in child env: %s", home, envText)
		}
		if !strings.Contains(envText, "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude")+"\n") {
			t.Fatalf("expected CLAUDE_CONFIG_DIR in child env: %s", envText)
		}
		if strings.Contains(envText, "must-not-leak") {
			t.Fatalf("secret leaked into child env: %s", envText)
		}
	})

	t.Run("resume_only_own_namespace_and_ephemeral", func(t *testing.T) {
		ch, err := provider.Stream(t.Context(), Request{
			UserID:    validUserID,
			Prompt:    "hi",
			SessionID: claudeSessionPrefix + testClaudeSessionID,
		})
		if err != nil {
			t.Fatalf("resume Stream failed: %v", err)
		}
		for range ch {
		}
		gotArgs, _ := os.ReadFile(argsRecord)
		if !strings.Contains(string(gotArgs), "--resume\n"+testClaudeSessionID+"\n") {
			t.Fatalf("expected --resume for Claude session, got: %s", gotArgs)
		}

		// Foreign namespace must start fresh without --resume.
		ch, err = provider.Stream(t.Context(), Request{
			UserID:    validUserID,
			Prompt:    "hi",
			SessionID: chatSessionPrefix + testClaudeSessionID,
		})
		if err != nil {
			t.Fatalf("foreign session Stream failed: %v", err)
		}
		for range ch {
		}
		gotArgs, _ = os.ReadFile(argsRecord)
		if strings.Contains(string(gotArgs), "--resume") {
			t.Fatalf("foreign session ID must not be resumed: %s", gotArgs)
		}

		// Ephemeral uses --no-session-persistence and returns empty SessionID.
		ch, err = provider.Stream(t.Context(), Request{
			UserID:    validUserID,
			Prompt:    "title",
			Ephemeral: true,
			SessionID: claudeSessionPrefix + testClaudeSessionID,
		})
		if err != nil {
			t.Fatalf("ephemeral Stream failed: %v", err)
		}
		var last Event
		for ev := range ch {
			last = ev
		}
		if last.Type != Done || last.SessionID != "" {
			t.Fatalf("ephemeral run should not emit session ID: %#v", last)
		}
		gotArgs, _ = os.ReadFile(argsRecord)
		if !strings.Contains(string(gotArgs), "--no-session-persistence") || strings.Contains(string(gotArgs), "--resume") {
			t.Fatalf("unexpected ephemeral flags: %s", gotArgs)
		}
	})

	t.Run("non_zero_exit_includes_stderr", func(t *testing.T) {
		failBin := filepath.Join(t.TempDir(), "claude-fail")
		if err := os.WriteFile(failBin, []byte("#!/bin/sh\necho 'stderr failure detail' >&2\nexit 2\n"), 0700); err != nil {
			t.Fatal(err)
		}
		ch, err := (ClaudeCliProvider{Bin: failBin}).Stream(t.Context(), Request{
			UserID: validUserID,
			Prompt: "hi",
		})
		if err != nil {
			t.Fatalf("Stream start failed: %v", err)
		}
		var events []Event
		for ev := range ch {
			events = append(events, ev)
		}
		if len(events) != 1 || events[0].Type != Error || events[0].Err == nil || !strings.Contains(events[0].Err.Error(), "stderr failure detail") {
			t.Fatalf("expected error with stderr tail, got %#v", events)
		}
	})

	t.Run("cancellation_kills_process_and_closes_channel", func(t *testing.T) {
		hangBin := filepath.Join(t.TempDir(), "claude-hang")
		hangScript := `#!/bin/sh
cat >/dev/null
printf '{"type":"system","subtype":"init","session_id":"` + testClaudeSessionID + `"}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"start"}}}\n'
exec sleep 30
`
		if err := os.WriteFile(hangBin, []byte(hangScript), 0700); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		ch, err := (ClaudeCliProvider{Bin: hangBin}).Stream(ctx, Request{
			UserID: validUserID,
			Prompt: "hi",
		})
		if err != nil {
			t.Fatalf("Stream start failed: %v", err)
		}
		select {
		case ev, ok := <-ch:
			if !ok || ev.Type != Text || ev.Delta != "start" {
				t.Fatalf("expected initial delta before cancel, got %#v ok=%v", ev, ok)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for initial delta")
		}
		cancel()
		select {
		case _, ok := <-ch:
			if ok {
				for range ch {
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("channel did not close promptly after cancellation")
		}
	})
}
