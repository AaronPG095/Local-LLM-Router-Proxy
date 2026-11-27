package config

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func defaultCredentialStorePath() string {
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		return filepath.Join(local, "LocalLLMRouter", "credentials.json")
	}
	return ""
}

// Windows PowerShell's ConvertFrom-SecureString stores a hex-encoded DPAPI blob.
func decryptDesktopCredential(value string) (string, error) {
	ciphertext, err := hex.DecodeString(value)
	if err != nil || len(ciphertext) == 0 {
		return "", errors.New("invalid DPAPI credential")
	}
	envelope := windows.DataBlob{Size: uint32(len(ciphertext)), Data: &ciphertext[0]}
	var opened windows.DataBlob
	flags := uint32(windows.CRYPTPROTECT_UI_FORBIDDEN)
	if err := windows.CryptUnprotectData(&envelope, nil, nil, 0, nil, flags, &opened); err != nil {
		return "", err
	}
	data := unsafe.Slice(opened.Data, int(opened.Size))
	defer func() {
		clear(data)
		windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(opened.Data))))
	}()
	if len(data)%2 != 0 {
		return "", errors.New("invalid DPAPI credential encoding")
	}
	utf16 := make([]uint16, len(data)/2)
	defer clear(utf16)
	for i := range utf16 {
		utf16[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return windows.UTF16ToString(utf16), nil
}
