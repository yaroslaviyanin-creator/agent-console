package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type AntigravityCliProvider struct {
	Bin          string
	DefaultModel string
}

func (p AntigravityCliProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if !userIDPattern.MatchString(req.UserID) {
		return nil, errors.New("invalid workspace user ID")
	}
	if len(req.Images) > 0 {
		return nil, ErrUnsupportedImageAttachments
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	workspace, err := chatWorkspace(filepath.Join(home, ".agent-console-agy"))
	if err != nil {
		return nil, err
	}
	if workspace != "/workspace" {
		home = filepath.Dir(workspace)
	}
	req.SessionID = chatSessionID(req.SessionID)
	args, err := antigravityArgs(req, p.DefaultModel)
	if err != nil {
		return nil, err
	}
	select {
	case antigravityChatSlot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	started := false
	defer func() {
		if !started {
			<-antigravityChatSlot
		}
	}()
	if err := writeAntigravityAgent(home, req.SystemInstructions); err != nil {
		return nil, err
	}
	bin := p.Bin
	if bin == "" {
		bin = "agy"
	}
	if strings.ContainsAny(bin, "/\\") && !filepath.IsAbs(bin) {
		bin, err = filepath.Abs(bin)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	var stderr stderrTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = append(antigravityEnvironment(), "HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, stderr.wrap("start antigravity exec", err)
	}
	started = true
	out := make(chan Event)
	go func() {
		defer close(out)
		terminal, parseErr := parseAgyJSONL(stdout, func(event Event) bool {
			return send(ctx, out, event)
		})
		if terminal.SessionID == "" && req.SessionID != "" {
			terminal.SessionID = req.SessionID
		}
		if parseErr != nil || terminal.Err != nil {
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		<-antigravityChatSlot
		switch {
		case ctx.Err() != nil:
			return
		case parseErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("parse antigravity exec", parseErr)}
		case terminal.Err != nil:
			terminal.Err = stderr.wrap("antigravity exec", terminal.Err)
		case waitErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("antigravity exec", waitErr)}
		}
		if terminal.Type == Done && terminal.SessionID != "" {
			terminal.SessionID = chatSessionPrefix + terminal.SessionID
		}
		send(ctx, out, terminal)
	}()
	return out, nil
}

// The custom agent definition stays fixed for the life of the subprocess.
var antigravityChatSlot = make(chan struct{}, 1)

const antigravityAgent = "agent-console-chat"

const agyChatOnlyInstructions = `---
name: agent-console-chat
description: Conversation without external tools
mainAgent: true
subagent: false
commandExecutionPolicy: "off"
tools: []
skills: []
---
# Core instructions

Отвечай в режиме «Только чат». Сообщай, что внешние инструменты, файлы и приложения недоступны, если для запроса нужен доступ к ним.
`

func writeAntigravityAgent(home, instructions string) error {
	dir := filepath.Join(home, ".gemini", "config", "agents")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	content := agyChatOnlyInstructions
	if instructions != "" {
		content += "\n" + instructions
	}
	return writePrivateFile(filepath.Join(dir, antigravityAgent+".md"), []byte(content))
}

const agyChatOnlySettings = `{"toolPermission":"strict","allowNonWorkspaceAccess":false,"permissions":{"allow":[],"ask":[],"deny":["command(*)","unsandboxed(*)","write_file(*)","read_file(*)","read_url(*)","execute_url(*)","mcp(*)"]}}`

func antigravityArgs(req Request, defaultModel string) ([]string, error) {
	model := req.Model
	if model == "" || model == "default" || model == "antigravity" {
		model = defaultModel
	}
	if model == "" {
		model = "gemini-3.8-flash-high"
	}
	if strings.HasPrefix(model, "-") {
		return nil, errors.New("invalid model name")
	}
	args := []string{
		"--settings", agyChatOnlySettings,
		"--agent", antigravityAgent,
		"--mode", "plan", "--sandbox", "--disable-slash-commands",
		"--output-format", "stream-json",
		"--model", model,
	}
	if req.SessionID != "" {
		if strings.HasPrefix(req.SessionID, "-") || !sessionPattern.MatchString(req.SessionID) {
			return nil, errors.New("invalid Antigravity session ID")
		}
		args = append(args, "--conversation", req.SessionID)
	}
	return append(args, "--print="+req.Prompt), nil
}

func antigravityEnvironment() []string {
	var env []string
	for _, key := range []string{
		"PATH",
		"SYSTEMROOT", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
		"AGY_ACCOUNT", "AGY_LOG", "AGY_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY",
		"AG_LS_PROXY",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type agyEvent struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversation_id"`
	Init           *struct {
		Model string `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepType     string    `json:"step_type"`
		TextDelta    string    `json:"text_delta"`
		ThoughtDelta string    `json:"thought_delta"`
		State        string    `json:"state"`
		Usage        *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		ConversationID string `json:"conversation_id"`
		Status         string `json:"status"`
		Response       string `json:"response"`
		Error          string `json:"error"`
		FinishedReason string `json:"finished_reason"`
		DeniedActions  []struct {
			Action      string `json:"action"`
			DisplayName string `json:"display_name"`
		} `json:"denied_actions"`
		Usage *agyUsage `json:"usage"`
	} `json:"result"`
}

type agyUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ThinkingTokens      int64 `json:"thinking_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	ThoughtsTokenCount  int64 `json:"thoughtsTokenCount"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	OutputTokensDetails *struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func parseAgyJSONL(input io.Reader, emit func(Event) bool) (Event, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 32<<10), 2<<20)
	var sessionID string
	var completed bool
	var usage *Usage
	var hasText bool

	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if len(text) == 0 {
			continue
		}
		if !strings.HasPrefix(text, "{") {
			continue
		}
		var line agyEvent
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			continue
		}
		if line.ConversationID != "" {
			sessionID = line.ConversationID
		}
		switch line.Event {
		case "init":
			if line.ConversationID != "" {
				sessionID = line.ConversationID
			}
		case "step_update":
			if line.StepUpdate != nil {
				su := line.StepUpdate
				if su.ThoughtDelta != "" {
					if !emit(Event{Type: Thinking, Delta: su.ThoughtDelta}) {
						return Event{}, context.Canceled
					}
				}
				if su.TextDelta != "" {
					if su.StepType == "thought" || su.StepType == "thinking" {
						if !emit(Event{Type: Thinking, Delta: su.TextDelta}) {
							return Event{}, context.Canceled
						}
					} else {
						hasText = true
						if !emit(Event{Type: Text, Delta: su.TextDelta}) {
							return Event{}, context.Canceled
						}
					}
				}
			}
		case "result":
			if line.Result != nil {
				res := line.Result
				if res.ConversationID != "" {
					sessionID = res.ConversationID
				}
				if res.Status == "ERROR" {
					msg := res.Error
					if msg == "" {
						msg = "Antigravity reported an error"
					}
					return Event{Type: Error, Err: errors.New(msg)}, nil
				}
				if res.Status == "SUCCESS" {
					completed = true
					if res.Usage != nil {
						reasoningTokens := max(res.Usage.ThinkingTokens, res.Usage.ReasoningTokens, res.Usage.ThoughtsTokenCount)
						if res.Usage.OutputTokensDetails != nil && res.Usage.OutputTokensDetails.ReasoningTokens > reasoningTokens {
							reasoningTokens = res.Usage.OutputTokensDetails.ReasoningTokens
						}
						usage = &Usage{
							InputTokens:       res.Usage.InputTokens,
							CachedInputTokens: res.Usage.CacheReadTokens,
							OutputTokens:      res.Usage.OutputTokens,
							ReasoningTokens:   reasoningTokens,
						}
					}
					if !hasText && res.Response != "" {
						hasText = true
						if !emit(Event{Type: Text, Delta: res.Response}) {
							return Event{}, context.Canceled
						}
					}
					if !hasText && len(res.DeniedActions) > 0 {
						return Event{Type: Error, Err: errors.New("Antigravity attempted a blocked tool action in headless chat mode")}, nil
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Event{}, err
	}
	if !completed || !hasText || sessionID == "" {
		return Event{}, errors.New("Antigravity stream ended before a complete response")
	}
	return Event{Type: Done, SessionID: sessionID, Usage: usage}, nil
}

func parseAgyModelsOutput(output, defaultModel string) []Model {
	var models []Model
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.Contains(line, "\t") {
			continue
		}
		id, name, _ := strings.Cut(line, "\t")
		id = strings.TrimSpace(id)
		name = strings.TrimSpace(name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, Model{
			ID:        id,
			Name:      name,
			IsDefault: id == defaultModel,
		})
	}
	if len(models) > 0 {
		hasDefault := false
		for _, m := range models {
			if m.IsDefault {
				hasDefault = true
				break
			}
		}
		if !hasDefault {
			models[0].IsDefault = true
		}
	}
	return models
}

func (p AntigravityCliProvider) ListModels(ctx context.Context) ([]Model, error) {
	defaultModel := p.DefaultModel
	if defaultModel == "" {
		defaultModel = "gemini-3.8-flash-high"
	}
	if raw := os.Getenv("ANTIGRAVITY_MODELS"); raw != "" {
		var list []Model
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, name, found := strings.Cut(part, ":")
			id = strings.TrimSpace(id)
			if !found || strings.TrimSpace(name) == "" {
				name = id
			} else {
				name = strings.TrimSpace(name)
			}
			list = append(list, Model{ID: id, Name: name, IsDefault: id == defaultModel})
		}
		if len(list) > 0 {
			hasDefault := false
			for _, m := range list {
				if m.IsDefault {
					hasDefault = true
					break
				}
			}
			if !hasDefault {
				list[0].IsDefault = true
			}
			return list, nil
		}
	}

	bin := p.Bin
	if bin == "" {
		bin = "agy"
	}
	cmd := exec.CommandContext(ctx, bin, "models")
	cmd.Env = append(antigravityEnvironment(), "HOME="+os.Getenv("HOME"))
	out, err := cmd.Output()
	if err == nil {
		models := parseAgyModelsOutput(string(out), defaultModel)
		if len(models) > 0 {
			return models, nil
		}
		slog.Warn("agy models returned no models; using fallback catalog", "bin", bin)
	} else {
		slog.Warn("agy models failed; using fallback catalog", "bin", bin, "error", err)
	}

	return fallbackAntigravityModels(defaultModel), nil
}

func fallbackAntigravityModels(defaultModel string) []Model {
	if defaultModel == "" {
		defaultModel = "gemini-3.8-flash-high"
	}
	baseModels := []struct {
		id   string
		name string
	}{
		{"gemini-4-hydrogen-high", "Gemini 4 Hydrogen (High)"},
		{"gemini-4-hydrogen-xhigh", "Gemini 4 Hydrogen (XHigh)"},
		{"gemini-4-hydrogen-medium", "Gemini 4 Hydrogen (Medium)"},
		{"gemini-4-hydrogen-low", "Gemini 4 Hydrogen (Low)"},
		{"gemini-3.8-flash-high", "Gemini 3.8 Flash (High)"},
		{"gemini-3.8-flash-medium", "Gemini 3.8 Flash (Medium)"},
		{"gemini-3.8-flash-low", "Gemini 3.8 Flash (Low)"},
		{"claude-sonnet-4-6", "Claude Sonnet 4.6 (Thinking)"},
		{"claude-opus-4-6-thinking", "Claude Opus 4.6 (Thinking)"},
		{"gpt-oss-120b-medium", "GPT-OSS 120B (Medium)"},
	}
	models := make([]Model, 0, len(baseModels)+1)
	foundDefault := false
	for _, bm := range baseModels {
		isDef := bm.id == defaultModel
		if isDef {
			foundDefault = true
		}
		models = append(models, Model{
			ID:        bm.id,
			Name:      bm.name,
			IsDefault: isDef,
		})
	}
	if !foundDefault {
		models = append([]Model{{
			ID:        defaultModel,
			Name:      defaultModel,
			IsDefault: true,
		}}, models...)
	}
	return models
}
