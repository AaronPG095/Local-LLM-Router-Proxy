//go:build !windows

package config

import "errors"

func defaultCredentialStorePath() string { return "" }

func decryptDesktopCredential(string) (string, error) {
	return "", errors.New("DPAPI credentials require Windows")
}
