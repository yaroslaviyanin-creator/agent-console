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

type GeminiCliProvider struct {
	Bin           string
	DefaultModel  string
	WorkspaceRoot string
}

func (p GeminiCliProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if !userIDPattern.MatchString(req.UserID) {
		return nil, errors.New("invalid workspace user ID")
	}
	args, err := geminiArgs(req, p.DefaultModel)
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
	if err := writeGeminiInstructions(root, req.SystemInstructions); err != nil {
		return nil, err
	}
	if err := ensureGeminiSettings(root); err != nil {
		return nil, err
	}
	bin := p.Bin
	if bin == "" {
		bin = "gemini"
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
	cmd.Env = geminiEnvironment()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, stderr.wrap("start gemini exec", err)
	}
	started = true
	out := make(chan Event)
	go func() {
		defer close(out)
		terminal, parseErr := parseGeminiJSONL(stdout, func(event Event) bool {
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
			terminal = Event{Type: Error, Err: stderr.wrap("parse gemini exec", parseErr)}
		case terminal.Err != nil:
			terminal.Err = stderr.wrap("gemini exec", terminal.Err)
		case waitErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("gemini exec", waitErr)}
		}
		send(ctx, out, terminal)
	}()
	return out, nil
}

func writeGeminiInstructions(root, instructions string) error {
	staleAgents := filepath.Join(root, "AGENTS.md")
	if err := os.Remove(staleAgents); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	path := filepath.Join(root, "GEMINI.md")
	if instructions == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	file, err := os.CreateTemp(root, ".gemini-instructions-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(instructions)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func ensureGeminiSettings(root string) error {
	dir := filepath.Join(root, ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		settings := []byte("{\n  \"tools\": {\n    \"allowed\": []\n  }\n}\n")
		return os.WriteFile(path, settings, 0600)
	}
	return nil
}

func geminiArgs(req Request, defaultModel string) ([]string, error) {
	model := req.Model
	if model == "" || model == "default" || model == "gemini" {
		model = defaultModel
	}
	if model == "" {
		model = "gemini-3.8-flash"
	}
	if strings.HasPrefix(model, "-") {
		return nil, errors.New("invalid model name")
	}
	args := []string{
		"--skip-trust",
		"--approval-mode", "plan",
		"-o", "stream-json",
		"-m", model,
	}
	if req.SessionID != "" {
		if strings.HasPrefix(req.SessionID, "-") || !sessionPattern.MatchString(req.SessionID) {
			return nil, errors.New("invalid Gemini session ID")
		}
		args = append(args, "-r", req.SessionID)
	}
	return append(args, "-p", req.Prompt), nil
}

func geminiEnvironment() []string {
	var env []string
	for _, key := range []string{
		"PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME",
		"SYSTEMROOT", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GEMINI_CLI_HOME", "NODE_EXTRA_CA_CERTS",
		"GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_GENAI_USE_GCA",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	env = append(env, "GEMINI_CLI_TRUST_WORKSPACE=true")
	return env
}

type geminiLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Delta     bool   `json:"delta"`
	Status    string `json:"status"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Error     *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Stats *struct {
		TotalTokens  int64 `json:"total_tokens"`
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
		Cached       int64 `json:"cached"`
		DurationMs   int64 `json:"duration_ms"`
	} `json:"stats"`
}

func parseGeminiJSONL(input io.Reader, emit func(Event) bool) (Event, error) {
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
		var line geminiLine
		if err := json.Unmarshal([]byte(text), &line); err != nil {
			continue
		}
		switch line.Type {
		case "init":
			if line.SessionID != "" {
				sessionID = line.SessionID
			}
		case "message":
			switch line.Role {
			case "assistant", "model", "agent":
				if line.Content != "" {
					hasText = true
					if !emit(Event{Type: Text, Delta: line.Content}) {
						return Event{}, context.Canceled
					}
				}
			case "thought":
				if line.Content != "" {
					if !emit(Event{Type: Thinking, Delta: line.Content}) {
						return Event{}, context.Canceled
					}
				}
			}
		case "thought":
			if line.Content != "" {
				if !emit(Event{Type: Thinking, Delta: line.Content}) {
					return Event{}, context.Canceled
				}
			}
		case "result":
			if line.Status == "error" {
				msg := "Gemini reported an error"
				if line.Error != nil && line.Error.Message != "" {
					msg = line.Error.Message
				}
				return Event{Type: Error, Err: errors.New(msg)}, nil
			}
			if line.Status == "success" {
				completed = true
				if line.Stats != nil {
					usage = &Usage{
						InputTokens:       line.Stats.InputTokens,
						CachedInputTokens: line.Stats.Cached,
						OutputTokens:      line.Stats.OutputTokens,
					}
				}
			}
		case "error":
			if line.Severity == "error" || line.Severity == "" {
				msg := line.Message
				if msg == "" {
					msg = "Gemini reported a stream error"
				}
				return Event{Type: Error, Err: errors.New(msg)}, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Event{}, err
	}
	if !completed || !hasText || sessionID == "" {
		return Event{}, errors.New("Gemini stream ended before a complete response")
	}
	return Event{Type: Done, SessionID: sessionID, Usage: usage}, nil
}

func (p GeminiCliProvider) ListModels(ctx context.Context) ([]Model, error) {
	defaultModel := p.DefaultModel
	if defaultModel == "" {
		defaultModel = "gemini-3.8-flash"
	}
	if raw := os.Getenv("GEMINI_MODELS"); raw != "" {
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
	baseModels := []struct {
		id   string
		name string
	}{
		{defaultModel, defaultModel},
		{"gemini-2.5-pro", "Gemini 2.5 Pro"},
		{"gemini-2.5-flash", "Gemini 2.5 Flash"},
		{"gemini-3.1-flash-lite", "Gemini 3.1 Flash Lite"},
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
