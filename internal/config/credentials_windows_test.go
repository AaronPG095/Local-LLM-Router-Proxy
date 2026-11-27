package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWindowsCredentialSetupAndRotation(t *testing.T) {
	powerShell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "setup-credentials.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	t.Setenv("LLM_ROUTER_TEST_SETUP", script)
	t.Setenv("LLM_ROUTER_TEST_STORE", path)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	run := func(rotate bool) {
		t.Helper()
		if rotate {
			t.Setenv("LLM_ROUTER_TEST_ROTATE", "1")
		} else {
			t.Setenv("LLM_ROUTER_TEST_ROTATE", "")
		}
		command := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "RemoteSigned", "-Command", `
$ErrorActionPreference = 'Stop'
function Read-Host {
    param([string]$Prompt, [switch]$AsSecureString)
    $secret = [System.Security.SecureString]::new()
    $value = switch ($Prompt) {
        'OPENAI_API_KEY' { if ($env:LLM_ROUTER_TEST_ROTATE) { 'replacement' } else { 'original' } }
        'ANTHROPIC_API_KEY' { if (-not $env:LLM_ROUTER_TEST_ROTATE) { 'preserved' } }
    }
    if ($value) { foreach ($char in $value.ToCharArray()) { $secret.AppendChar($char) } }
    $secret
}
& $env:LLM_ROUTER_TEST_SETUP -StorePath $env:LLM_ROUTER_TEST_STORE
`)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture credential setup failed: %v\n%s", err, result)
		}
	}
	assertKeys := func(openAI string) {
		t.Helper()
		cfg := Config{Providers: map[string]ProviderConfig{
			"openai":    {APIKeyEnv: "OPENAI_API_KEY"},
			"anthropic": {APIKeyEnv: "ANTHROPIC_API_KEY"},
		}}
		if err := cfg.LoadCredentials(path); err != nil {
			t.Fatal(err)
		}
		if cfg.ResolveAPIKey("openai") != openAI || cfg.ResolveAPIKey("anthropic") != "preserved" {
			t.Fatal("saved keys do not match the expected rotation")
		}
	}
	run(false)
	assertKeys("original")
	run(true)
	assertKeys("replacement")
}
