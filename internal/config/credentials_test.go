package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialStorePrecedence(t *testing.T) {
	t.Setenv("TEST_ROUTER_OVERRIDE", "environment-value")
	t.Setenv("TEST_ROUTER_SAVED", "")
	cfg := Config{Providers: map[string]ProviderConfig{
		"override": {APIKeyEnv: "TEST_ROUTER_OVERRIDE"},
		"saved":    {APIKeyEnv: "TEST_ROUTER_SAVED"},
	}}
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"TEST_ROUTER_OVERRIDE":"ignored","TEST_ROUTER_SAVED":"ciphertext","OTHER":"ignored"}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := cfg.loadCredentialStore(path, false, func(blob string) (string, error) {
		if blob != "ciphertext" {
			t.Fatal("unexpected credential entry passed to decryptor")
		}
		return "saved-value", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResolveAPIKey("override") != "environment-value" || cfg.ResolveAPIKey("saved") != "saved-value" {
		t.Fatal("expected environment override and saved fallback")
	}
}

func TestCredentialStoreErrorsDoNotExposeValues(t *testing.T) {
	t.Setenv("TEST_ROUTER_SAVED", "")
	cfg := Config{Providers: map[string]ProviderConfig{"saved": {APIKeyEnv: "TEST_ROUTER_SAVED"}}}
	path := filepath.Join(t.TempDir(), "missing.json")
	decrypt := func(string) (string, error) { return "", errors.New("secret data in external error") }
	if err := cfg.loadCredentialStore(path, true, decrypt); err != nil {
		t.Fatal(err)
	}
	if err := cfg.loadCredentialStore(path, false, decrypt); err == nil {
		t.Fatal("an explicit missing credential store should fail")
	}
	for _, data := range []string{`{"TEST_ROUTER_SAVED":`, `{"TEST_ROUTER_SAVED":"secret value"}`, "null"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		err := cfg.loadCredentialStore(path, false, decrypt)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("expected a sanitized credential store error")
		}
	}
}
