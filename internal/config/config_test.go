package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadValidConfigAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "router.config.yaml")

	yamlContent := `
providers:
  gemini:
    type: gemini
    base_url: "https://generativelanguage.googleapis.com"
    api_key_env: "TEST_GEMINI_KEY"
    pricing_per_1m:
      gemini-2.5-flash: { input: 0.15, output: 0.60, cached_input: 0.0375 }
budgets:
  session:
    max_tokens: 10000
    hard_action: "block"
`
	if err := os.WriteFile(cfgPath, []byte(yamlContent), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected default host 127.0.0.1, got %q", cfg.Server.Host)
	}
	if cfg.Server.Port != 8787 {
		t.Errorf("expected default port 8787, got %d", cfg.Server.Port)
	}
	if cfg.Telemetry.SQLitePath != "./data/router.db" {
		t.Errorf("expected default sqlite_path, got %q", cfg.Telemetry.SQLitePath)
	}
	if cfg.Budgets.Session.SoftWarnPct != 0.80 {
		t.Errorf("expected default soft_warn_pct 0.80, got %v", cfg.Budgets.Session.SoftWarnPct)
	}

	t.Setenv("TEST_GEMINI_KEY", "secret-123")
	if got := cfg.ResolveAPIKey("gemini"); got != "secret-123" {
		t.Errorf("ResolveAPIKey() = %q, want secret-123", got)
	}
	if p, ok := cfg.LookupPricing("gemini", "gemini-2.5-flash"); !ok || p.Input != 0.15 {
		t.Errorf("LookupPricing() = %+v, %v", p, ok)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "missing providers",
			yaml: "server:\n  port: 8787\n",
		},
		{
			name: "invalid port",
			yaml: "server:\n  port: 70000\nproviders:\n  gemini:\n    type: gemini\n    base_url: http://localhost\n",
		},
		{
			name: "invalid hard_action",
			yaml: "providers:\n  gemini:\n    type: gemini\n    base_url: http://localhost\nbudgets:\n  session:\n    hard_action: explode\n",
		},
		{
			name: "downgrade without target",
			yaml: "providers:\n  gemini:\n    type: gemini\n    base_url: http://localhost\nbudgets:\n  session:\n    hard_action: downgrade\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "bad.yaml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(p); err == nil {
				t.Fatalf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}
