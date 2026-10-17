//go:build !windows

package settings

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path follows BMP's intentionally simple Unix convention: ~/.bmp.json.
func Path() (string, error) {
	if override := os.Getenv("BMP_SETTINGS_PATH"); override != "" {
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".bmp.json"), nil
}
