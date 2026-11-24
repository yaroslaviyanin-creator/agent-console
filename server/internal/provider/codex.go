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
	"time"
)

type CodexCliProvider struct {
	Bin           string
	WorkspaceRoot string
}

var (
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
	root := filepath.Join(p.WorkspaceRoot, req.UserID)
	if err := os.MkdirAll(root, 0700); err != nil {
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
	if model := req.Model; model != "" && model != "codex" && model != "default" {
		args = append(args, "--model="+model)
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
	return Event{Type: Done, SessionID: sessionID}, nil
}
