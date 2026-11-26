package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProviderValidation(t *testing.T) {
	t.Setenv("RUNNING_IN_DOCKER", "true")

	for _, valid := range []string{"stub", "codex", "antigravity"} {
		t.Setenv("PROVIDER", valid)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load(%q) unexpected error: %v", valid, err)
		}
		if cfg.Provider != valid {
			t.Fatalf("Load(%q) provider = %q", valid, cfg.Provider)
		}
	}

	for _, unimplemented := range []string{"claude", "paid_api"} {
		t.Setenv("PROVIDER", unimplemented)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("Load(%q) expected not implemented error, got: %v", unimplemented, err)
		}
	}

	t.Setenv("PROVIDER", "bogus")
	if _, err := Load(); err == nil {
		t.Fatalf("Load(\"bogus\") expected error, got nil")
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
