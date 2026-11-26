package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadProviderValidation(t *testing.T) {
	t.Setenv("RUNNING_IN_DOCKER", "true")

	for _, valid := range []string{"stub", "codex", "antigravity", "claude"} {
		t.Setenv("PROVIDER", valid)
		t.Setenv("ENABLED_PROVIDERS", "")
		t.Setenv("DEFAULT_PROVIDER", "")
		t.Setenv("MEMORY_PROVIDER", "")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(%q) unexpected error: %v", valid, err)
		}
		if cfg.Provider != valid || cfg.DefaultProvider != valid || cfg.MemoryProvider != valid || !reflect.DeepEqual(cfg.EnabledProviders, []string{valid}) {
			t.Fatalf("Load(%q) unexpected provider config: %+v", valid, cfg)
		}
	}

	t.Setenv("PROVIDER", "paid_api")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("Load(\"paid_api\") expected not implemented error, got: %v", err)
	}

	t.Setenv("PROVIDER", "bogus")
	if _, err := Load(); err == nil {
		t.Fatalf("Load(\"bogus\") expected error, got nil")
	}
}

func TestParseProviderConfigMulti(t *testing.T) {
	t.Parallel()

	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := m[k]
			return v, ok
		}
	}

	cfg, err := ParseProviderConfig(env(map[string]string{
		"ENABLED_PROVIDERS": "antigravity, codex, claude",
		"DEFAULT_PROVIDER":  "codex",
		"MEMORY_PROVIDER":   "antigravity",
		"PROVIDER":          "paid_api", // ignored when ENABLED_PROVIDERS is set
	}))
	if err != nil {
		t.Fatalf("ParseProviderConfig unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.EnabledProviders, []string{"antigravity", "codex", "claude"}) ||
		cfg.DefaultProvider != "codex" || cfg.MemoryProvider != "antigravity" {
		t.Fatalf("unexpected provider config: %+v", cfg)
	}

	// Defaults to first enabled provider.
	cfg, err = ParseProviderConfig(env(map[string]string{
		"ENABLED_PROVIDERS": "claude,stub",
	}))
	if err != nil {
		t.Fatalf("ParseProviderConfig defaults unexpected error: %v", err)
	}
	if cfg.DefaultProvider != "claude" || cfg.MemoryProvider != "claude" {
		t.Fatalf("expected defaults claude/claude, got %+v", cfg)
	}

	// Legacy fallback when ENABLED_PROVIDERS is empty.
	cfg, err = ParseProviderConfig(env(map[string]string{}))
	if err != nil {
		t.Fatalf("ParseProviderConfig empty unexpected error: %v", err)
	}
	if !reflect.DeepEqual(cfg.EnabledProviders, []string{"stub"}) || cfg.DefaultProvider != "stub" || cfg.MemoryProvider != "stub" {
		t.Fatalf("unexpected legacy empty config: %+v", cfg)
	}

	bad := []struct {
		name string
		env  map[string]string
	}{
		{"duplicate", map[string]string{"ENABLED_PROVIDERS": "codex,codex"}},
		{"empty entry", map[string]string{"ENABLED_PROVIDERS": "codex,"}},
		{"paid_api in enabled", map[string]string{"ENABLED_PROVIDERS": "codex,paid_api"}},
		{"unknown in enabled", map[string]string{"ENABLED_PROVIDERS": "codex,gemini"}},
		{"default not enabled", map[string]string{"ENABLED_PROVIDERS": "codex,claude", "DEFAULT_PROVIDER": "antigravity"}},
		{"default paid_api", map[string]string{"ENABLED_PROVIDERS": "codex", "DEFAULT_PROVIDER": "paid_api"}},
		{"memory not enabled", map[string]string{"ENABLED_PROVIDERS": "codex,claude", "MEMORY_PROVIDER": "stub"}},
		{"memory unknown", map[string]string{"ENABLED_PROVIDERS": "codex", "MEMORY_PROVIDER": "bogus"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseProviderConfig(env(tc.env)); err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

func TestLoadDockerAndHostCLIGuard(t *testing.T) {
	origDockerenvPath := dockerenvPath
	t.Cleanup(func() {
		dockerenvPath = origDockerenvPath
	})

	setOutsideDocker := func(t *testing.T) {
		t.Helper()
		t.Setenv("RUNNING_IN_DOCKER", "")
		t.Setenv("ALLOW_HOST_CLI_EXECUTION", "")
		t.Setenv("ENABLED_PROVIDERS", "")
		t.Setenv("DEFAULT_PROVIDER", "")
		t.Setenv("MEMORY_PROVIDER", "")
		dockerenvPath = filepath.Join(t.TempDir(), "missing-dockerenv")
	}

	t.Run("stub allowed outside docker", func(t *testing.T) {
		setOutsideDocker(t)
		t.Setenv("PROVIDER", "stub")
		if _, err := Load(); err != nil {
			t.Fatalf("Load(stub) unexpected error: %v", err)
		}
	})

	t.Run("non-stub refused outside docker", func(t *testing.T) {
		for _, provider := range []string{"codex", "antigravity", "claude"} {
			setOutsideDocker(t)
			t.Setenv("PROVIDER", provider)
			t.Setenv("RUNNING_IN_DOCKER", "TRUE")
			t.Setenv("ALLOW_HOST_CLI_EXECUTION", "1")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "refusing to run CLI provider outside Docker; use docker compose") {
				t.Fatalf("Load(%q) expected docker guard error, got: %v", provider, err)
			}
		}
		setOutsideDocker(t)
		t.Setenv("ENABLED_PROVIDERS", "stub,codex")
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "refusing to run CLI provider outside Docker; use docker compose") {
			t.Fatalf("Load(stub,codex) expected docker guard error, got: %v", err)
		}
	})

	t.Run("non-stub allowed with RUNNING_IN_DOCKER", func(t *testing.T) {
		setOutsideDocker(t)
		t.Setenv("RUNNING_IN_DOCKER", "true")
		t.Setenv("PROVIDER", "codex")
		if _, err := Load(); err != nil {
			t.Fatalf("Load(codex) with RUNNING_IN_DOCKER=true unexpected error: %v", err)
		}
	})

	t.Run("non-stub allowed with dockerenv file", func(t *testing.T) {
		setOutsideDocker(t)
		marker := filepath.Join(t.TempDir(), ".dockerenv")
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatalf("write dockerenv marker: %v", err)
		}
		dockerenvPath = marker
		t.Setenv("PROVIDER", "antigravity")
		if _, err := Load(); err != nil {
			t.Fatalf("Load(antigravity) with .dockerenv unexpected error: %v", err)
		}
	})

	t.Run("non-stub allowed with ALLOW_HOST_CLI_EXECUTION", func(t *testing.T) {
		setOutsideDocker(t)
		t.Setenv("ALLOW_HOST_CLI_EXECUTION", "true")
		t.Setenv("PROVIDER", "codex")
		if _, err := Load(); err != nil {
			t.Fatalf("Load(codex) with ALLOW_HOST_CLI_EXECUTION=true unexpected error: %v", err)
		}
	})
}
