package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type CodexCliProvider struct {
	Bin           string
	WorkspaceRoot string
}

var (
	// Keep the context file stable for the lifetime of each CLI subprocess.
	workspaceLocks sync.Map
	userIDPattern  = regexp.MustCompile(`^[a-f0-9]{32}$`)
	sessionPattern = regexp.MustCompile(`^[[:xdigit:]]{8}(-[[:xdigit:]]{4}){3}-[[:xdigit:]]{12}$`)
)

func (p CodexCliProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if !userIDPattern.MatchString(req.UserID) {
		return nil, errors.New("invalid workspace user ID")
	}
	args, err := codexArgs(req)
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
	if err := writeUserContext(root, req.SystemInstructions); err != nil {
		return nil, err
	}
	bin := p.Bin
	if strings.ContainsAny(bin, "/\\") && !filepath.IsAbs(bin) {
		bin, err = filepath.Abs(bin)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(req.Prompt)
	var stderr stderrTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	cmd.Env = cliEnvironment()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, stderr.wrap("start codex exec", err)
	}
	started = true
	out := make(chan Event)
	go func() {
		defer close(out)
		terminal, parseErr := parseJSONL(stdout, func(event Event) bool {
			return send(ctx, out, event)
		})
		if parseErr != nil || terminal.Err != nil {
			// A malformed or failed stream must not leave a subprocess holding stdout open.
			_ = cmd.Process.Kill()
		}
		waitErr := cmd.Wait()
		<-slot
		switch {
		case ctx.Err() != nil:
			return
		case parseErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("parse codex exec", parseErr)}
		case terminal.Err != nil:
			terminal.Err = stderr.wrap("codex exec", terminal.Err)
		case waitErr != nil:
			terminal = Event{Type: Error, Err: stderr.wrap("codex exec", waitErr)}
		}
		send(ctx, out, terminal)
	}()
	return out, nil
}

// Replace the file atomically with a new private inode, including when
// upgrading a workspace that already has a file with broader permissions.
func writeUserContext(root, instructions string) error {
	path := filepath.Join(root, "USER_CONTEXT.md")
	if instructions == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	file, err := os.CreateTemp(root, ".user-context-*")
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

// os/exec writes stderr from one goroutine. Read the tail after Wait.
type stderrTail struct {
	data [8 << 10]byte
	size int
}

func (t *stderrTail) Write(p []byte) (int, error) {
	n := len(p)
	if n >= len(t.data) {
		copy(t.data[:], p[n-len(t.data):])
		t.size = len(t.data)
		return n, nil
	}
	if overflow := t.size + n - len(t.data); overflow > 0 {
		t.size = copy(t.data[:], t.data[overflow:t.size])
	}
	t.size += copy(t.data[t.size:], p)
	return n, nil
}

func (t *stderrTail) wrap(operation string, err error) error {
	if tail := strings.TrimSpace(string(t.data[:t.size])); tail != "" {
		return fmt.Errorf("%s: %w (stderr: %q)", operation, err, tail)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func codexArgs(req Request) ([]string, error) {
	args := []string{
		"exec", "--ignore-user-config", "--skip-git-repo-check", "--json",
		"-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`, "--disable", "shell_tool",
		"--disable", "plugins", "--disable", "multi_agent", "--disable", "multi_agent_v2",
	}
	// Codex CLI accepts developer_instructions with --ignore-user-config.
	// Supply it as a config string so the original prompt stays on stdin.
	if req.SystemInstructions != "" {
		instructions, err := json.Marshal(req.SystemInstructions)
		if err != nil {
			return nil, err
		}
		args = append(args, "-c", "developer_instructions="+string(instructions))
	}
	if req.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if model := req.Model; model != "" && model != "codex" && model != "default" {
		args = append(args, "--model="+model)
	}
	if effort := strings.ToLower(strings.TrimSpace(req.Reasoning)); effort != "" && effort != "default" {
		switch effort {
		case "minimal", "low", "medium", "high", "xhigh":
			args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
		default:
			return nil, errors.New("invalid Codex reasoning effort")
		}
	}
	if req.SessionID != "" {
		if !sessionPattern.MatchString(req.SessionID) {
			return nil, errors.New("invalid Codex session ID")
		}
		args = append(args, "resume", req.SessionID)
	}
	return append(args, "-"), nil
}

func cliEnvironment() []string {
	var env []string
	for _, key := range []string{
		"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "CODEX_HOME", "OPENAI_API_KEY",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type codexLine struct {
	Usage     *Usage `json:"usage"`
	Type      string `json:"type"`
	ThreadID  string `json:"thread_id"`
	SessionID string `json:"session_id"`
	Item      struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
}

func parseJSONL(input io.Reader, emit func(Event) bool) (Event, error) {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 32<<10), 2<<20)
	var sessionID string
	var completed bool
	var usage *Usage
	reasoning := make(map[string]string)
	delivered := make(map[string]bool)
	for scanner.Scan() {
		if len(strings.TrimSpace(scanner.Text())) == 0 {
			continue
		}
		var line codexLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil || line.Type == "" {
			return Event{}, errors.New("invalid Codex JSONL event")
		}
		switch line.Type {
		case "thread.started", "session.started", "session.created":
			sessionID = line.ThreadID
			if sessionID == "" {
				sessionID = line.SessionID
			}
			if !sessionPattern.MatchString(sessionID) {
				return Event{}, errors.New("missing or invalid Codex session ID")
			}
		case "item.updated", "item.completed":
			item := line.Item
			if item.ID == "" || item.Text == "" {
				continue
			}
			switch {
			case item.Type == "reasoning":
				before := reasoning[item.ID]
				if delta, ok := strings.CutPrefix(item.Text, before); ok && delta != "" {
					if !emit(Event{Type: Thinking, Delta: delta}) {
						return Event{}, context.Canceled
					}
					reasoning[item.ID] = item.Text
				}
			case item.Type == "agent_message" && line.Type == "item.completed" && !delivered[item.ID]:
				if !emit(Event{Type: Text, Delta: item.Text}) {
					return Event{}, context.Canceled
				}
				delivered[item.ID] = true
			}
		case "turn.completed":
			completed = true
			if line.Usage != nil {
				usage = line.Usage
			}
		case "turn.failed", "error":
			return Event{Type: Error, Err: errors.New("Codex reported a failed turn")}, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return Event{}, err
	}
	if !completed || len(delivered) == 0 || sessionID == "" {
		return Event{}, errors.New("Codex stream ended before a complete response")
	}
	return Event{Type: Done, SessionID: sessionID, Usage: usage}, nil
}
