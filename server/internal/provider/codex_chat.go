package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const codexChatConfig = `approval_policy = "never"
default_permissions = "chat"
web_search = "disabled"
cli_auth_credentials_store = "file"
project_doc_max_bytes = 0
mcp_servers = {}

[permissions.chat.filesystem]
":root" = "deny"

[apps._default]
enabled = false
open_world_enabled = false

[agents]
enabled = false

[features]
apps = false
plugins = false
remote_plugin = false
shell_tool = false
unified_exec = false
view_image = false
multi_agent_v2 = false
browser_use = false
computer_use = false
in_app_browser = false
code_mode_host = false
hooks = false
image_generation = false
skill_search = false
tool_suggest = false
goals = false
skip_host_skill_discovery = true
workspace_dependencies = false
`

const codexChatInstructions = `You are the conversational assistant in Agent Console. Answer the user's questions in their language.
This chat has no access to local files, shell, subagents, external apps, connectors, or web search. If a request needs that access, explain the limitation directly and answer from any information the user provided.
`

const chatSessionPrefix = "chat-v1:"

func chatSessionID(stored string) string {
	id, isolated := strings.CutPrefix(stored, chatSessionPrefix)
	if isolated {
		return id
	}
	return ""
}

func NewCodexCliProvider(ctx context.Context, bin string) (CodexCliProvider, error) {
	bin, err := exec.LookPath(bin)
	if err != nil {
		return CodexCliProvider{}, err
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return CodexCliProvider{}, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return CodexCliProvider{}, err
	}
	authHome := os.Getenv("CODEX_HOME")
	if authHome == "" {
		authHome = filepath.Join(userHome, ".codex")
	}
	home := filepath.Join(userHome, ".codex-chat")
	workspace, err := chatWorkspace(home)
	if err != nil {
		return CodexCliProvider{}, err
	}
	if err := prepareCodexHome(ctx, bin, home, authHome, workspace); err != nil {
		return CodexCliProvider{}, err
	}
	return CodexCliProvider{Bin: bin, home: home, workspace: workspace}, nil
}

func chatWorkspace(base string) (string, error) {
	path := "/workspace"
	if _, err := os.Stat("/.dockerenv"); os.Getenv("RUNNING_IN_DOCKER") == "true" || err == nil {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", errors.New("/workspace must be a directory")
		}
		return path, nil
	}
	path = filepath.Join(base, "workspace")
	return path, os.MkdirAll(path, 0700)
}

func prepareCodexHome(ctx context.Context, bin, home, authHome, workspace string) error {
	if err := os.MkdirAll(home, 0700); err != nil {
		return err
	}
	if err := seedCodexAuth(home, authHome); err != nil {
		return err
	}
	// Build the generation catalog from this binary, so model-specific tools
	// cannot enable apply_patch. Account-visible model discovery is separate.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "models", "--bundled")
	cmd.Dir = workspace
	cmd.Env = codexChatEnvironment(home)
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read bundled Codex model catalog: %w", err)
	}
	catalog, err := codexChatModelCatalog(raw)
	if err != nil {
		return err
	}
	catalogPath := filepath.Join(home, "chat-models.json")
	if err := writePrivateFile(catalogPath, catalog); err != nil {
		return err
	}
	if err := writePrivateFile(filepath.Join(home, "chat-instructions.md"), []byte(codexChatInstructions)); err != nil {
		return err
	}
	return writePrivateFile(filepath.Join(home, "config.toml"), []byte(codexChatConfig))
}

func codexChatModelCatalog(raw []byte) ([]byte, error) {
	var catalog struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("parse Codex model catalog: %w", err)
	}
	if len(catalog.Models) == 0 {
		return nil, errors.New("bundled Codex model catalog is empty")
	}
	for _, model := range catalog.Models {
		if model == nil {
			return nil, errors.New("invalid bundled Codex model")
		}
		delete(model, "apply_patch_tool_type")
		model["experimental_supported_tools"] = json.RawMessage("[]")
		model["tool_mode"] = json.RawMessage(`"direct"`)
	}
	return json.Marshal(catalog)
}

func seedCodexAuth(home, authHome string) error {
	path := filepath.Join(home, "auth.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	auth, err := os.ReadFile(filepath.Join(authHome, "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return writePrivateFile(path, auth)
}

func codexChatEnvironment(home string) []string {
	var env []string
	for _, entry := range cliEnvironment() {
		if !strings.HasPrefix(entry, "CODEX_HOME=") {
			env = append(env, entry)
		}
	}
	return append(env, "CODEX_HOME="+home)
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".chat-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
