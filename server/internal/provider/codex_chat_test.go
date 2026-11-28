package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestCodexChatConfigNoTools(t *testing.T) {
	for _, want := range []string{
		`approval_policy = "never"`,
		`default_permissions = "chat"`,
		`web_search = "live"`,
		`project_doc_max_bytes = 0`,
		`mcp_servers = {}`,
		`":root" = "deny"`,
		`enabled = false`,
		`open_world_enabled = false`,
		`apps = false`,
		`plugins = false`,
		`remote_plugin = false`,
		`shell_tool = false`,
		`unified_exec = false`,
		`view_image = false`,
		`[agents]
enabled = false`,
		`multi_agent_v2 = false`,
		`browser_use = false`,
		`computer_use = false`,
		`in_app_browser = false`,
		`code_mode_host = false`,
		`hooks = false`,
		`image_generation = false`,
		`skill_search = false`,
		`tool_suggest = false`,
		`workspace_dependencies = false`,
	} {
		if !strings.Contains(codexChatConfig, want) {
			t.Fatalf("codexChatConfig missing no-tools directive %q", want)
		}
	}
}

func TestCodexChatEnvironmentIsolatesCodexHome(t *testing.T) {
	originalHome := filepath.Join(t.TempDir(), "original-codex")
	isolatedHome := filepath.Join(t.TempDir(), "isolated-codex")
	t.Setenv("CODEX_HOME", originalHome)
	t.Setenv("OPENAI_API_KEY", "test-key")

	env := codexChatEnvironment(isolatedHome)
	if !slices.Contains(env, "CODEX_HOME="+isolatedHome) {
		t.Fatalf("expected isolated CODEX_HOME=%s in env: %v", isolatedHome, env)
	}
	if slices.Contains(env, "CODEX_HOME="+originalHome) {
		t.Fatalf("original CODEX_HOME leaked into env: %v", env)
	}
	if !slices.Contains(env, "OPENAI_API_KEY=test-key") {
		t.Fatalf("expected OPENAI_API_KEY preserved in env: %v", env)
	}
}

func TestSeedCodexAuthCopiesOnlyAuth(t *testing.T) {
	authHome := t.TempDir()
	home := t.TempDir()

	authPayload := []byte(`{"token":"secret"}`)
	if err := os.WriteFile(filepath.Join(authHome, "auth.json"), authPayload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authHome, "config.toml"), []byte("model = \"o3\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authHome, "history.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := seedCodexAuth(home, authHome); err != nil {
		t.Fatalf("seedCodexAuth failed: %v", err)
	}
	gotAuth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("expected auth.json to be copied: %v", err)
	}
	if string(gotAuth) != string(authPayload) {
		t.Fatalf("auth.json mismatch: got %q, want %q", gotAuth, authPayload)
	}
	for _, unexpected := range []string{"config.toml", "history.jsonl"} {
		if _, err := os.Stat(filepath.Join(home, unexpected)); !os.IsNotExist(err) {
			t.Fatalf("expected %s not to be copied, stat err: %v", unexpected, err)
		}
	}

	// Existing auth.json must not be overwritten.
	existingAuth := []byte(`{"token":"existing"}`)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), existingAuth, 0600); err != nil {
		t.Fatal(err)
	}
	if err := seedCodexAuth(home, authHome); err != nil {
		t.Fatalf("seedCodexAuth with existing auth failed: %v", err)
	}
	gotAuth, err = os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotAuth) != string(existingAuth) {
		t.Fatalf("existing auth.json was overwritten: got %q", gotAuth)
	}

	// Missing source auth.json is allowed.
	emptyHome := t.TempDir()
	if err := seedCodexAuth(emptyHome, t.TempDir()); err != nil {
		t.Fatalf("seedCodexAuth with missing source failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(emptyHome, "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no auth.json when source missing, stat err: %v", err)
	}
}

func TestPrepareCodexHomeIsolatedConfigAndAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX shell script")
	}

	authHome := t.TempDir()
	home := filepath.Join(t.TempDir(), "codex-chat")
	workspace := t.TempDir()

	if err := os.WriteFile(filepath.Join(authHome, "auth.json"), []byte(`{"auth":"ok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authHome, "config.toml"), []byte("user_only = true\n"), 0600); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
if [ "$1" = "debug" ] && [ "$2" = "models" ] && [ "$3" = "--bundled" ]; then
  printf '{"models":[{"slug":"gpt-5","display_name":"GPT-5","apply_patch_tool_type":"freeform","experimental_supported_tools":["shell"]}]}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	if err := prepareCodexHome(t.Context(), fakeBin, home, authHome, workspace); err != nil {
		t.Fatalf("prepareCodexHome failed: %v", err)
	}

	gotAuth, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil || string(gotAuth) != `{"auth":"ok"}` {
		t.Fatalf("unexpected auth.json: %q err=%v", gotAuth, err)
	}

	configBytes, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(configBytes) != codexChatConfig {
		t.Fatalf("config.toml differs from the chat policy: %q", configBytes)
	}
	catalogBytes, err := os.ReadFile(filepath.Join(home, "chat-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(catalogBytes), "apply_patch_tool_type") ||
		!strings.Contains(string(catalogBytes), `"experimental_supported_tools":[]`) {
		t.Fatalf("chat-models.json still enables tools: %s", catalogBytes)
	}
}

func TestCodexChatModelCatalogStripsTools(t *testing.T) {
	raw := []byte(`{
		"models": [
			{
				"slug": "gpt-5",
				"display_name": "GPT-5",
				"apply_patch_tool_type": "freeform",
				"experimental_supported_tools": ["exec_command", "apply_patch"]
			},
			{
				"slug": "gpt-5-mini",
				"display_name": "GPT-5 Mini"
			}
		]
	}`)

	cleaned, err := codexChatModelCatalog(raw)
	if err != nil {
		t.Fatalf("codexChatModelCatalog failed: %v", err)
	}

	var parsed struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(cleaned, &parsed); err != nil {
		t.Fatalf("unmarshal cleaned catalog: %v", err)
	}
	if len(parsed.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(parsed.Models))
	}
	for _, m := range parsed.Models {
		if _, ok := m["apply_patch_tool_type"]; ok {
			t.Fatalf("apply_patch_tool_type was not stripped: %v", m)
		}
		if tools, ok := m["experimental_supported_tools"].([]any); !ok || len(tools) != 0 {
			t.Fatalf("experimental_supported_tools was not emptied: %v", m)
		}
	}
	if parsed.Models[0]["slug"] != "gpt-5" || parsed.Models[0]["display_name"] != "GPT-5" {
		t.Fatalf("expected model metadata preserved: %v", parsed.Models[0])
	}

	for _, bad := range []string{
		`not-json`,
		`{"models":[]}`,
		`{"models":[null]}`,
	} {
		if _, err := codexChatModelCatalog([]byte(bad)); err == nil {
			t.Fatalf("expected error for %s, got nil", bad)
		}
	}
}

func TestChatSessionNamespace(t *testing.T) {
	if got := chatSessionID(chatSessionPrefix + testSessionID); got != testSessionID {
		t.Fatalf("expected stripped session ID %q, got %q", testSessionID, got)
	}
	for _, nonIsolated := range []string{"", testSessionID, "other-v1:" + testSessionID} {
		if got := chatSessionID(nonIsolated); got != "" {
			t.Fatalf("expected empty session ID for non-namespaced %q, got %q", nonIsolated, got)
		}
	}
}
