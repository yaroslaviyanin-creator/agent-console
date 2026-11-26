package provider

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	claudeSessionPrefix = "claude-chat-v1:"
	claudeDefaultModel  = "sonnet"

	claudeChatInstructions = `You are the conversational assistant in Agent Console. Answer the user's questions in their language.
This chat has no access to local files, shell, subagents, external apps, connectors, or web search. If a request needs that access, explain the limitation directly and answer from any information the user provided.`
)

// ClaudeCliProvider runs chat-only turns via the Claude Code CLI.
type ClaudeCliProvider struct {
	Bin string
}

// ListModels returns the built-in official Claude CLI model aliases.
func (p ClaudeCliProvider) ListModels(ctx context.Context) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []Model{
		{ID: "sonnet", Name: "Claude Sonnet", IsDefault: true},
		{ID: "opus", Name: "Claude Opus", IsDefault: false},
		{ID: "haiku", Name: "Claude Haiku", IsDefault: false},
	}, nil
}

// Stream executes a single Claude Code CLI turn and streams parsed events.
func (p ClaudeCliProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if !userIDPattern.MatchString(req.UserID) {
		return nil, errors.New("invalid workspace user ID")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	workspace, err := chatWorkspace(filepath.Join(home, ".claude-chat"))
	if err != nil {
		return nil, err
	}
	if req.Ephemeral {
		req.SessionID = ""
	} else {
		req.SessionID = claudeSessionID(req.SessionID)
	}
	args, err := claudeArgs(req)
	if err != nil {
		return nil, err
	}
	bin := p.Bin
	if bin == "" {
		bin = "claude"
	}
	if strings.ContainsAny(bin, "/\\") && !filepath.IsAbs(bin) {
		bin, err = filepath.Abs(bin)
		if err != nil {
			return nil, err
		}
	}
	stdinPayload, err := claudeStdinPayload(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	cmd.Stdin = strings.NewReader(stdinPayload)
	var stderr stderrTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = claudeEnvironment(home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, stderr.wrap("start claude exec", err)
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		terminal, parseErr := parseClaudeJSONL(stdout, func(event Event) bool {
			return send(ctx, out, event)
		})
		if parseErr != nil || terminal.Err != nil {
			// Ensure a malformed or errored stream does not leave a child holding stdout open.
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		switch {
		case ctx.Err() != nil:
			return
		case parseErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("parse claude exec", parseErr)}
		case terminal.Err != nil:
			terminal.Err = stderr.wrap("claude exec", terminal.Err)
		case waitErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("claude exec", waitErr)}
		}
		if terminal.Type == Done {
			if req.Ephemeral {
				terminal.SessionID = ""
			} else if terminal.SessionID != "" {
				terminal.SessionID = claudeSessionPrefix + terminal.SessionID
			}
		}
		send(ctx, out, terminal)
	}()
	return out, nil
}

func claudeSessionID(stored string) string {
	id, isolated := strings.CutPrefix(stored, claudeSessionPrefix)
	if isolated {
		return id
	}
	return ""
}

func claudeArgs(req Request) ([]string, error) {
	model := req.Model
	if model == "" || model == "default" || model == "claude" {
		model = claudeDefaultModel
	}
	if strings.HasPrefix(model, "-") {
		return nil, errors.New("invalid model name")
	}
	instructions := claudeChatInstructions
	if trimmed := strings.TrimSpace(req.SystemInstructions); trimmed != "" {
		instructions += "\n\n" + trimmed
	}
	args := []string{
		"-p",
		"--verbose",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--tools", "",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--restricted",
		"--safe-mode",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--no-chrome",
		"--permission-mode", "dontAsk",
		"--permission-prompts", "none",
		"--system-prompt-snapshot", "off",
		"--model", model,
		"--append-system-prompt", instructions,
	}
	if len(req.Images) > 0 {
		args = append(args, "--input-format", "stream-json")
	}
	switch effort := strings.ToLower(strings.TrimSpace(req.Reasoning)); effort {
	case "", "default":
	case "low", "medium", "high", "xhigh", "max":
		args = append(args, "--effort", effort)
	case "thinking":
		args = append(args, "--effort", "high")
	default:
		return nil, errors.New("invalid Claude reasoning effort")
	}
	if req.Ephemeral {
		args = append(args, "--no-session-persistence")
	} else if req.SessionID != "" {
		if !sessionPattern.MatchString(req.SessionID) {
			return nil, errors.New("invalid Claude session ID")
		}
		args = append(args, "--resume", req.SessionID)
	}
	return args, nil
}

func claudeStdinPayload(req Request) (string, error) {
	if len(req.Images) == 0 {
		return req.Prompt, nil
	}
	type imageSource struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	type contentBlock struct {
		Type   string       `json:"type"`
		Text   string       `json:"text,omitempty"`
		Source *imageSource `json:"source,omitempty"`
	}
	blocks := make([]contentBlock, 0, len(req.Images)+1)
	for _, img := range req.Images {
		blocks = append(blocks, contentBlock{
			Type: "image",
			Source: &imageSource{
				Type:      "base64",
				MediaType: img.MIMEType,
				Data:      base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	if req.Prompt != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: req.Prompt})
	}
	envelope := struct {
		Type    string `json:"type"`
		Message struct {
			Role    string         `json:"role"`
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}{
		Type: "user",
	}
	envelope.Message.Role = "user"
	envelope.Message.Content = blocks
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", err
	}
	return string(encoded) + "\n", nil
}

func claudeEnvironment(home string) []string {
	var env []string
	for _, key := range []string{
		"PATH", "SYSTEMROOT", "TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "LC_CTYPE", "USERPROFILE",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env,
		"HOME="+home,
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"),
	)
}

type claudeLine struct {
	Type      string             `json:"type"`
	Subtype   string             `json:"subtype"`
	SessionID string             `json:"session_id"`
	IsError   bool               `json:"is_error"`
	Result    string             `json:"result"`
	Errors    []string           `json:"errors"`
	Error     json.RawMessage    `json:"error"`
	Message   json.RawMessage    `json:"message"`
	Event     *claudeStreamEvent `json:"event"`
	Usage     *claudeUsage       `json:"usage"`
}

type claudeStreamEvent struct {
	Type    string `json:"type"`
	Message *struct {
		ID string `json:"id"`
	} `json:"message"`
	ContentBlock *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content_block"`
	Delta *struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"delta"`
}

type claudeAssistantMessage struct {
	ID      string `json:"id"`
	Content []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	} `json:"content"`
}

type claudeUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
}

type claudeMsgState struct {
	thinking strings.Builder
	text     strings.Builder
}

func getClaudeMsgState(states map[string]*claudeMsgState, key string) *claudeMsgState {
	if s := states[key]; s != nil {
		return s
	}
	s := &claudeMsgState{}
	states[key] = s
	return s
}

func parseClaudeErrorField(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return strings.TrimSpace(asString)
	}
	var asObject struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(raw, &asObject); err == nil {
		if msg := strings.TrimSpace(asObject.Message); msg != "" {
			return msg
		}
		return strings.TrimSpace(asObject.Type)
	}
	return ""
}

func parseClaudeJSONL(input io.Reader, emit func(Event) bool) (Event, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 32<<10), 2<<20)

	var sessionID string
	var completed bool
	var usage *Usage
	var hasText bool
	states := make(map[string]*claudeMsgState)
	activeKey := "_default"
	anonSeq := 0

	for scanner.Scan() {
		rawLine := strings.TrimSpace(scanner.Text())
		if rawLine == "" {
			continue
		}
		var line claudeLine
		if err := json.Unmarshal([]byte(rawLine), &line); err != nil || line.Type == "" {
			return Event{}, errors.New("invalid Claude JSONL event")
		}
		if line.SessionID != "" {
			if !sessionPattern.MatchString(line.SessionID) {
				return Event{}, errors.New("missing or invalid Claude session ID")
			}
			sessionID = line.SessionID
		}

		switch line.Type {
		case "error":
			msg := parseClaudeErrorField(line.Error)
			if msg == "" {
				msg = "Claude reported an error"
			}
			return Event{Type: Error, Err: errors.New(msg)}, nil

		case "stream_event":
			if line.Event == nil {
				continue
			}
			ev := line.Event
			switch ev.Type {
			case "message_start":
				if ev.Message != nil && ev.Message.ID != "" {
					activeKey = ev.Message.ID
				} else {
					anonSeq++
					activeKey = fmt.Sprintf("_anon_%d", anonSeq)
				}
			case "content_block_start":
				if ev.ContentBlock != nil {
					state := getClaudeMsgState(states, activeKey)
					switch ev.ContentBlock.Type {
					case "thinking":
						if ev.ContentBlock.Thinking != "" {
							if !emit(Event{Type: Thinking, Delta: ev.ContentBlock.Thinking}) {
								return Event{}, context.Canceled
							}
							state.thinking.WriteString(ev.ContentBlock.Thinking)
						}
					case "text":
						if ev.ContentBlock.Text != "" {
							hasText = true
							if !emit(Event{Type: Text, Delta: ev.ContentBlock.Text}) {
								return Event{}, context.Canceled
							}
							state.text.WriteString(ev.ContentBlock.Text)
						}
					}
				}
			case "content_block_delta":
				if ev.Delta != nil {
					state := getClaudeMsgState(states, activeKey)
					switch ev.Delta.Type {
					case "thinking_delta":
						if ev.Delta.Thinking != "" {
							if !emit(Event{Type: Thinking, Delta: ev.Delta.Thinking}) {
								return Event{}, context.Canceled
							}
							state.thinking.WriteString(ev.Delta.Thinking)
						}
					case "text_delta":
						if ev.Delta.Text != "" {
							hasText = true
							if !emit(Event{Type: Text, Delta: ev.Delta.Text}) {
								return Event{}, context.Canceled
							}
							state.text.WriteString(ev.Delta.Text)
						}
					}
				}
			}

		case "assistant":
			if errMsg := parseClaudeErrorField(line.Error); errMsg != "" {
				return Event{Type: Error, Err: errors.New(errMsg)}, nil
			}
			if len(line.Message) == 0 || string(line.Message) == "null" {
				continue
			}
			var msg claudeAssistantMessage
			if err := json.Unmarshal(line.Message, &msg); err != nil {
				return Event{}, errors.New("invalid Claude assistant message")
			}
			key := msg.ID
			if key == "" {
				key = activeKey
			} else if states[key] == nil && (activeKey == "_default" || strings.HasPrefix(activeKey, "_anon_")) && states[activeKey] != nil {
				states[key] = states[activeKey]
				delete(states, activeKey)
				activeKey = key
			}
			state := getClaudeMsgState(states, key)

			var fullThinking, fullText strings.Builder
			for _, block := range msg.Content {
				switch block.Type {
				case "thinking":
					fullThinking.WriteString(block.Thinking)
				case "text":
					fullText.WriteString(block.Text)
				}
			}
			if thinking := fullThinking.String(); thinking != "" {
				if delta, ok := strings.CutPrefix(thinking, state.thinking.String()); ok && delta != "" {
					if !emit(Event{Type: Thinking, Delta: delta}) {
						return Event{}, context.Canceled
					}
					state.thinking.WriteString(delta)
				}
			}
			if text := fullText.String(); text != "" {
				if delta, ok := strings.CutPrefix(text, state.text.String()); ok && delta != "" {
					hasText = true
					if !emit(Event{Type: Text, Delta: delta}) {
						return Event{}, context.Canceled
					}
					state.text.WriteString(delta)
				}
			}

		case "result":
			if line.IsError || (line.Subtype != "" && line.Subtype != "success") {
				var msg string
				for _, item := range line.Errors {
					if trimmed := strings.TrimSpace(item); trimmed != "" {
						if msg != "" {
							msg += "; "
						}
						msg += trimmed
					}
				}
				if msg == "" {
					msg = strings.TrimSpace(line.Result)
				}
				if msg == "" {
					msg = parseClaudeErrorField(line.Error)
				}
				if msg == "" {
					msg = strings.TrimSpace(line.Subtype)
				}
				if msg == "" {
					msg = "Claude reported an error"
				}
				return Event{Type: Error, Err: errors.New(msg)}, nil
			}
			completed = true
			if line.Usage != nil {
				usage = &Usage{
					InputTokens:       line.Usage.InputTokens + line.Usage.CacheReadInputTokens + line.Usage.CacheCreationInputTokens,
					CachedInputTokens: line.Usage.CacheReadInputTokens,
					OutputTokens:      line.Usage.OutputTokens,
				}
			}
			if !hasText && line.Result != "" {
				hasText = true
				if !emit(Event{Type: Text, Delta: line.Result}) {
					return Event{}, context.Canceled
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return Event{}, err
	}
	if !completed || !hasText || sessionID == "" {
		return Event{}, errors.New("Claude stream ended before a complete response")
	}
	return Event{Type: Done, SessionID: sessionID, Usage: usage}, nil
}
