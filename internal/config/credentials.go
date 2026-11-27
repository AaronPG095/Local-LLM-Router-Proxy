package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// LoadCredentials loads the desktop credential store before the router starts.
// An empty path uses the optional platform default. Environment keys take precedence.
func (c *Config) LoadCredentials(path string) error {
	optional := path == ""
	if optional {
		path = defaultCredentialStorePath()
	}
	if path == "" {
		return nil
	}
	return c.loadCredentialStore(path, optional, decryptDesktopCredential)
}

func (c *Config) loadCredentialStore(path string, optional bool, decrypt func(string) (string, error)) error {
	needed := make(map[string]bool)
	for _, provider := range c.Providers {
		if name := provider.APIKeyEnv; name != "" && os.Getenv(name) == "" {
			needed[name] = true
		}
	}
	if optional && len(needed) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read credential store: %w", err)
	}
	var encrypted map[string]string
	if err := json.Unmarshal(data, &encrypted); err != nil || encrypted == nil {
		return errors.New("invalid credential store: expected a JSON object of encrypted entries")
	}

	keys := make(map[string]string)
	for name := range needed {
		value, exists := encrypted[name]
		if !exists {
			continue
		}
		key, err := decrypt(value)
		key = strings.TrimSpace(key)
		if err != nil || key == "" {
			return fmt.Errorf("cannot open the saved credential for %s", name)
		}
		keys[name] = key
	}
	c.desktopKeys = keys
	return nil
}
