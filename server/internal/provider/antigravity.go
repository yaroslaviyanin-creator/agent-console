package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type AntigravityCliProvider struct {
	Bin           string
	DefaultModel  string
	WorkspaceRoot string
}

func (p AntigravityCliProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if !userIDPattern.MatchString(req.UserID) {
		return nil, errors.New("invalid workspace user ID")
	}
	args, err := antigravityArgs(req, p.DefaultModel)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(filepath.Join(p.WorkspaceRoot, req.UserID))
	if err != nil {
		return nil, err
	}
	lock, _ := workspaceLocks.LoadOrStore(root, make(chan struct{}, 1))
	slot := lock.(chan struct{})
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	started := false
	defer func() {
		if !started {
			<-slot
		}
	}()
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := writeAntigravityInstructions(root, req.SystemInstructions); err != nil {
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
	cmd.Dir = root
	var stderr stderrTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = antigravityEnvironment()
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
		<-slot
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
		send(ctx, out, terminal)
	}()
	return out, nil
}

const agyChatOnlyInstructions = "Режим работы: «Только чат» (chat-only). Все инструменты работы с файлами, терминалом, браузером, поиском и MCP отключены политикой песочницы. Не пытайся вызывать инструменты и не ссылайся на локальные файлы проекта; если пользователь просит прочитать/изменить файлы или выполнить команду, сразу объясни текстом, что в этом режиме у тебя нет доступа к инструментам, и предложи прислать код/текст прямо в чат."

func writeAntigravityInstructions(root, instructions string) error {
	// Remove stale GEMINI.md (e.g. left over from GeminiCliProvider) so
	// Antigravity only reads the active AGENTS.md context in this workspace.
	staleGemini := filepath.Join(root, "GEMINI.md")
	if err := os.Remove(staleGemini); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	content := agyChatOnlyInstructions
	if instructions != "" {
		content += "\n\n" + instructions
	}
	path := filepath.Join(root, "AGENTS.md")
	file, err := os.CreateTemp(root, ".agy-instructions-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(content)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

const agyChatOnlySettings = `{"permissions":{"deny":["command(*)","unsandboxed(*)","write_file(*)","read_file(*)","read_url(*)","execute_url(*)","mcp(*)","browser(*)","subagent(*)"]}}`

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
		"--output-format", "stream-json",
		"--model", model,
	}
	if req.SessionID != "" {
		if strings.HasPrefix(req.SessionID, "-") || !sessionPattern.MatchString(req.SessionID) {
			return nil, errors.New("invalid Antigravity session ID")
		}
		args = append(args, "--conversation", req.SessionID)
	}
	return append(args, "--print", req.Prompt), nil
}

func antigravityEnvironment() []string {
	var env []string
	for _, key := range []string{
		"PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME",
		"SYSTEMROOT", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
		"AGY_ACCOUNT", "AGY_LOG", "AGY_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY",
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
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ThinkingTokens  int64 `json:"thinking_tokens"`
	CacheReadTokens int64 `json:"cache_read_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
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
						usage = &Usage{
							InputTokens:       res.Usage.InputTokens,
							CachedInputTokens: res.Usage.CacheReadTokens,
							OutputTokens:      res.Usage.OutputTokens,
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
	cmd.Env = antigravityEnvironment()
	out, err := cmd.Output()
	if err == nil {
		models := parseAgyModelsOutput(string(out), defaultModel)
		if len(models) > 0 {
			return models, nil
		}
	}

	baseModels := []struct {
		id   string
		name string
	}{
		{defaultModel, defaultModel},
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
	var models []Model
	seen := make(map[string]bool)
	for _, bm := range baseModels {
		if seen[bm.id] {
			continue
		}
		seen[bm.id] = true
		models = append(models, Model{
			ID:        bm.id,
			Name:      bm.name,
			IsDefault: bm.id == defaultModel,
		})
	}
	return models, nil
}
