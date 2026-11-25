package config

import (
	"strings"
	"testing"
)

func TestLoadProviderValidation(t *testing.T) {
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
